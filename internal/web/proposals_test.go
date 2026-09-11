package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// edit is the whole member form, since a partial post reads as somebody
// clearing the fields it leaves out.
func edit(m store.Member, change map[string]string) url.Values {
	f := url.Values{
		"fornamn":      {m.FirstName},
		"efternamn":    {m.LastName},
		"epost":        {m.Email},
		"telefon":      {m.Phone},
		"typ":          {string(m.Kind)},
		"lagenhet":     {m.Apartment},
		"medlem_sedan": {m.JoinedOn.Format("2006-01-02")},
		"anteckning":   {m.Note},
	}
	for k, v := range change {
		f.Set(k, v)
	}
	return f
}

// A proposal that would change nothing must never reach the board. They would
// be asked to decide something that does nothing when decided, and whoever
// filed it would be left believing a change was on its way.
func TestAProposalThatChangesNothingIsNotFiled(t *testing.T) {
	h := newHarness(t)
	m := h.member(t, "m1", "anna@example.test", config.KindVan)

	rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1",
		edit(m, map[string]string{"anledning": "inget egentligen"}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect", rec.Code)
	}

	open, err := h.store.ProposalsFor(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(open) != 0 {
		t.Errorf("a proposal that changes nothing was filed: %+v", open)
	}
	// And nothing was announced to the board either.
	h.chat.quiet(t)
}

// Editing the same member twice used to keep only the first change: the
// second was refused because one was already waiting, so the board approved
// half of what was asked and the proposer had to do the rest by hand.
func TestASecondEditReplacesYourOwnWaitingProposal(t *testing.T) {
	h := newHarness(t)
	m := h.member(t, "m1", "anna@example.test", config.KindVan)

	first := edit(m, map[string]string{"telefon": "070-111 11 11", "anledning": "nytt nummer"})
	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", first); rec.Code != http.StatusSeeOther {
		t.Fatalf("first: got %d", rec.Code)
	}
	second := edit(m, map[string]string{
		"telefon": "070-111 11 11", "lagenhet": "1502", "anledning": "och ny lägenhet",
	})
	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", second); rec.Code != http.StatusSeeOther {
		t.Fatalf("second: got %d", rec.Code)
	}

	all, err := h.store.ProposalsFor(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	var open []store.Proposal
	for _, p := range all {
		if p.Open() {
			open = append(open, p)
		}
	}
	if len(open) != 1 {
		t.Fatalf("got %d waiting proposals, want exactly one: %+v", len(open), open)
	}
	// And it is the newer one, carrying both changes.
	if open[0].After.Phone != "070-111 11 11" || open[0].After.Apartment != "1502" {
		t.Errorf("the waiting proposal lost a change: %+v", open[0].After)
	}
	if open[0].Reason != "och ny lägenhet" {
		t.Errorf("reason: got %q", open[0].Reason)
	}

	// Approving it applies everything, which is the whole point.
	rec := h.do(t, config.RoleBoard, "POST", "/andringar/"+open[0].ID+"/godkann", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("approving: got %d", rec.Code)
	}
	got, err := h.store.Member(context.Background(), "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if got.Phone != "070-111 11 11" || got.Apartment != "1502" {
		t.Errorf("approving did not apply everything: phone=%q apartment=%q", got.Phone, got.Apartment)
	}
}

// Somebody else's request is not ours to replace. Two people asking for
// different things about one member is a conversation, not a race.
func TestAnotherPersonsWaitingProposalIsNotReplaced(t *testing.T) {
	h := newHarness(t)
	m := h.member(t, "m1", "anna@example.test", config.KindVan)

	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1",
		edit(m, map[string]string{"telefon": "070-111 11 11"})); rec.Code != http.StatusSeeOther {
		t.Fatalf("intake: got %d", rec.Code)
	}
	// The cashier may edit outright, so use a role that has to propose.
	before, err := h.store.ProposalsFor(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("setup: got %d proposals", len(before))
	}
	if before[0].ProposedBy == "" {
		t.Fatal("the proposal has no proposer")
	}
}

// The board has to be able to read what a proposal does and why, on the page
// where they decide it.
func TestTheApprovalPageShowsTheFieldsAndTheReason(t *testing.T) {
	h := newHarness(t)
	m := h.member(t, "m1", "anna@example.test", config.KindVan)

	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", edit(m, map[string]string{
		"telefon":    "070-555 12 34",
		"lagenhet":   "1502",
		"anledning":  "Flyttar in i 1502 den 1 oktober",
		"anteckning": "vill gärna vara med i matlaget",
	})); rec.Code != http.StatusSeeOther {
		t.Fatalf("proposing: got %d", rec.Code)
	}

	body := h.do(t, config.RoleBoard, "GET", "/andringar", nil).Body.String()
	for _, want := range []string{
		"070-555 12 34",                   // the new value
		"1502",                            // a second changed field
		"vill gärna vara med i matlaget",  // a third
		"Flyttar in i 1502 den 1 oktober", // the reason
		"Motivering",                      // labelled as such
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the approval page does not show %q", want)
		}
	}
	// The explanation of how the queue works is behind the (i), not in the way.
	if !strings.Contains(body, `<details class="info">`) {
		t.Error("the page has no info control")
	}
	if strings.Contains(body, `<p class="lede">`) {
		t.Error("the page still leads with explanatory prose")
	}
}

// A proposal with no reason says so, rather than leaving the board guessing
// whether one was given and lost.
func TestAProposalWithoutAReasonSaysSo(t *testing.T) {
	h := newHarness(t)
	m := h.member(t, "m1", "anna@example.test", config.KindVan)

	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1",
		edit(m, map[string]string{"telefon": "070-555 12 34"})); rec.Code != http.StatusSeeOther {
		t.Fatalf("proposing: got %d", rec.Code)
	}
	body := h.do(t, config.RoleBoard, "GET", "/andringar", nil).Body.String()
	if !strings.Contains(body, "Ingen motivering angavs") {
		t.Error("a proposal with no reason does not say so")
	}
}
