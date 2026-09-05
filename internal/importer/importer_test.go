package importer

import (
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/google"
)

func card(name, given, family, email, phone string) google.Person {
	p := google.Person{ResourceName: "people/c1"}
	if name != "" || given != "" || family != "" {
		p.Names = []google.Name{{DisplayName: name, GivenName: given, FamilyName: family}}
	}
	if email != "" {
		p.Emails = []google.Email{{Value: email}}
	}
	if phone != "" {
		p.Phones = []google.Phone{{Value: phone}}
	}
	return p
}

func TestCandidateReadsAContactCard(t *testing.T) {
	c := candidateOf(card("Anna Andersson", "Anna", "Andersson",
		"Anna.Andersson@Example.TEST", "070-1"), config.KindBo, "Bomedlemmar")

	if c.FirstName != "Anna" || c.LastName != "Andersson" {
		t.Errorf("name: %q %q", c.FirstName, c.LastName)
	}
	if c.Email != "anna.andersson@example.test" {
		t.Errorf("the address was not normalised: %q", c.Email)
	}
	if c.Phone != "070-1" {
		t.Errorf("phone: %q", c.Phone)
	}
	if c.Kind != config.KindBo {
		t.Errorf("kind: %q", c.Kind)
	}
	if c.Problem != "" {
		t.Errorf("a perfectly good card was rejected: %q", c.Problem)
	}
	if !c.Importable() {
		t.Error("a perfectly good card is not importable")
	}
}

// Plenty of real cards carry only a display name. Splitting on the last space
// is right far more often than it is wrong for a Swedish name, and a wrong
// split is visible and fixable on the member's own page afterwards.
func TestACardWithOnlyADisplayNameIsSplit(t *testing.T) {
	tests := []struct {
		display, first, last string
	}{
		{"Anna Andersson", "Anna", "Andersson"},
		{"Anna Maria Andersson", "Anna Maria", "Andersson"},
		{"Cher", "Cher", ""},
	}
	for _, tc := range tests {
		c := candidateOf(card(tc.display, "", "", "a@example.test", ""),
			config.KindVan, "Vänmedlemmar")
		if c.FirstName != tc.first || c.LastName != tc.last {
			t.Errorf("%q split into %q / %q, want %q / %q",
				tc.display, c.FirstName, c.LastName, tc.first, tc.last)
		}
	}
}

// The interesting output of a migration is not the rows that worked.
func TestCardsThatCannotBecomeMembersSayWhy(t *testing.T) {
	tests := []struct {
		name string
		card google.Person
	}{
		{"no address at all", card("Anna Andersson", "Anna", "Andersson", "", "070-1")},
		{"an address that is not one", card("Anna", "Anna", "", "anna-at-example", "")},
		{"no name at all", card("", "", "", "anna@example.test", "")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := candidateOf(tc.card, config.KindBo, "Bomedlemmar")
			if c.Problem == "" {
				t.Error("the card was accepted; it should have been flagged")
			}
			if c.Importable() {
				t.Error("a flagged card is still importable")
			}
		})
	}
}

func TestTheResultSortsCandidatesIntoThreePiles(t *testing.T) {
	r := Result{Candidates: []Candidate{
		{FirstName: "Ready", Email: "a@example.test"},
		{FirstName: "Already", Email: "b@example.test", Existing: true},
		{FirstName: "Broken", Email: "", Problem: "ingen e-postadress"},
		// A card that is both already in the register and broken counts as
		// broken: it is the one that needs a human.
		{FirstName: "Both", Email: "c@example.test", Existing: true, Problem: "trasig"},
	}}
	if got := len(r.Ready()); got != 1 {
		t.Errorf("ready: got %d, want 1", got)
	}
	if got := len(r.Skipped()); got != 1 {
		t.Errorf("skipped: got %d, want 1", got)
	}
	if got := len(r.Problems()); got != 2 {
		t.Errorf("problems: got %d, want 2", got)
	}
}

func TestCandidateNameJoinsWhatIsThere(t *testing.T) {
	if got := (Candidate{FirstName: "Anna", LastName: "Andersson"}).Name(); got != "Anna Andersson" {
		t.Errorf("got %q", got)
	}
	if got := (Candidate{FirstName: "Cher"}).Name(); got != "Cher" {
		t.Errorf("got %q", got)
	}
	if got := (Candidate{LastName: "Andersson"}).Name(); got != "Andersson" {
		t.Errorf("got %q", got)
	}
}
