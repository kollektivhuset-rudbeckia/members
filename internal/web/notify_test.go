package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// Somebody filling in the public page must reach a human quickly, so the
// interview team hears about it in their own channel straight away.
func TestAnInterestRegistrationReachesTheInterviewTeam(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", url.Values{
		"fornamn":    {"Åsa"},
		"efternamn":  {"Öberg"},
		"epost":      {"asa@example.test"},
		"telefon":    {"070-123 45 67"},
		"varfor":     {"middagar"},
		"meddelande": {"Jag hörde om huset av en vän."},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d, want a redirect", rec.Code)
	}

	h.chat.waitFor(t, 1)
	got := h.chat.only(t, "Åsa Öberg")
	if got.Channel != "chan-candidates" {
		t.Errorf("channel: got %q, want the interview team's", got.Channel)
	}
	for _, want := range []string{
		"Ny intresseanmälan", "asa@example.test", "070-123 45 67",
		"Gemensamma middagar", "Jag hörde om huset av en vän.", "/kandidater",
	} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, got.Message)
		}
	}
	// It must not go to the board's channel: they do not need a message every
	// time a stranger fills in a form.
	if strings.Contains(got.Channel, "registry") {
		t.Error("an interest registration went to the board's channel")
	}
}

// A change ny@ cannot make outright becomes a proposal, and the board has to
// know it is waiting — that is the whole point of the queue.
func TestAProposalTellsTheBoardItIsWaiting(t *testing.T) {
	h := newHarness(t)
	m := h.member(t, "m1", "anna@example.test", config.KindVan)

	form := url.Values{
		"fornamn": {m.FirstName}, "efternamn": {m.LastName},
		"epost": {m.Email}, "typ": {"bo"},
		"medlem_sedan": {"2025-06-01"}, "anledning": {"flyttar in i 42"},
	}
	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("proposing: got %d", rec.Code)
	}

	h.chat.waitFor(t, 1)
	got := h.chat.only(t, "Föreslagen ändring")
	if got.Channel != "chan-registry" {
		t.Errorf("channel: got %q, want the board's", got.Channel)
	}
	for _, want := range []string{"Anna Andersson", "intervjugruppen", "ny@example.test",
		"Väntar på styrelsen", "/andringar"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, got.Message)
		}
	}
}

// Welcoming somebody is a new member, and the board sees it like any other.
func TestWelcomingACandidateIsAnnouncedAsANewMember(t *testing.T) {
	h := newHarness(t)
	id := h.candidate(t, "Vera", "vera@example.test", "new")

	if rec := h.do(t, config.RoleIntake, "POST", "/kandidater/"+id+"/valkomna",
		url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("welcoming: got %d", rec.Code)
	}

	h.chat.waitFor(t, 1)
	got := h.chat.only(t, "Ny medlem")
	if got.Channel != "chan-registry" {
		t.Errorf("channel: got %q, want the board's", got.Channel)
	}
	for _, want := range []string{"Vera", "från kandidatlistan", "Vänmedlem", "/medlem/"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, got.Message)
		}
	}
}

// The board and the cashier change the register directly, and those changes
// are announced too — the channel is a record of what happened, not only of
// what somebody asked for.
func TestChangesByTheBoardAndTheCashierAreAnnounced(t *testing.T) {
	for _, role := range []config.Role{config.RoleBoard, config.RoleCashier} {
		t.Run(string(role), func(t *testing.T) {
			h := newHarness(t)
			m := h.member(t, "m1", "anna@example.test", config.KindVan)

			form := url.Values{
				"fornamn": {m.FirstName}, "efternamn": {m.LastName},
				"epost": {m.Email}, "telefon": {"070-000 00 00"}, "typ": {"van"},
				"medlem_sedan": {"2025-06-01"},
			}
			if rec := h.do(t, role, "POST", "/medlem/m1", form); rec.Code != http.StatusSeeOther {
				t.Fatalf("saving: got %d", rec.Code)
			}

			h.chat.waitFor(t, 1)
			got := h.chat.only(t, "Ändrad medlem")
			if got.Channel != "chan-registry" {
				t.Errorf("channel: got %q", got.Channel)
			}
			// The line says who, in the house's words as well as the address.
			who := map[config.Role]string{
				config.RoleBoard: "styrelsen", config.RoleCashier: "kassören",
			}[role]
			if !strings.Contains(got.Message, who) {
				t.Errorf("the message does not say %q did it:\n%s", who, got.Message)
			}
			// And what changed, so the board can read it without opening the page.
			if !strings.Contains(got.Message, "telefon") {
				t.Errorf("the message does not say what changed:\n%s", got.Message)
			}
		})
	}
}

// Fees are recorded in the trail but not announced. A cashier ticking off a
// hundred of them in February would otherwise be a hundred messages, and they
// are the cashiers' business rather than the board's.
func TestFeesAreRecordedButNotAnnounced(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindVan)

	rec := h.do(t, config.RoleCashier, "POST", "/medlem/m1/betalning",
		url.Values{"ar": {"2026"}, "betald": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("recording a fee: got %d", rec.Code)
	}
	h.chat.quiet(t)

	// It is in the trail all the same.
	entries, err := h.store.Audit(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "payment.recorded" {
			found = true
		}
	}
	if !found {
		t.Error("the fee was not written to the audit trail either")
	}
}

// A name is typed into a public form by a stranger, and the message is
// Markdown. Nobody should be able to end a table row early, forge a heading,
// or make the message say something the register did not.
func TestAMessageCannotBeRearrangedByWhatSomebodyTypes(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", url.Values{
		"fornamn":   {"Eva|**Styrelsen**"},
		"efternamn": {"Test"},
		"epost":     {"eva@example.test"},
		"varfor":    {"middagar"},
		// A newline in a note would otherwise break out of the table.
		"meddelande": {"rad ett\n| **E-post** | forged@example.test |"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}

	h.chat.waitFor(t, 1)
	got := h.chat.sent()[0]

	// Nothing a stranger typed may appear as a line of the registry's own
	// message. Their words are allowed, quoted; their layout is not.
	for _, line := range strings.Split(got.Message, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "|") && strings.Contains(line, "forged@") {
			t.Errorf("a typed value became a table row:\n%s", got.Message)
		}
		if strings.Contains(line, "forged@") && !strings.HasPrefix(strings.TrimSpace(line), ">") {
			t.Errorf("a typed line escaped the quote:\n%s", got.Message)
		}
	}
	if !strings.Contains(got.Message, `Eva\|`) {
		t.Errorf("the pipe in the name was not escaped:\n%s", got.Message)
	}
	if n := strings.Count(got.Message, "eva@example.test"); n != 1 {
		t.Errorf("the real address appears %d times:\n%s", n, got.Message)
	}
}

// While the messages are being written they go to one person instead of the
// house, and the message says which channel it would have gone to — the
// routing is the part worth checking and it is invisible otherwise.
func TestTestModeSendsADirectMessageInsteadOfAnnouncing(t *testing.T) {
	h := newHarness(t)
	h.server.rt.Chat.TestUser = "u-mikael"

	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", url.Values{
		"fornamn": {"Nils"}, "efternamn": {"Test"},
		"epost": {"nils@example.test"}, "varfor": {"middagar"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}

	h.chat.waitFor(t, 1)
	got := h.chat.sent()[0]
	if got.DMTo != "u-mikael" {
		t.Errorf("went to %q as a channel post, want a direct message", got.Channel)
	}
	if !strings.Contains(got.Message, "chan-candidates") {
		t.Errorf("the test message does not name the channel it would have used:\n%s", got.Message)
	}
	if !strings.Contains(got.Message, "Nils Test") {
		t.Errorf("the test message lost its content:\n%s", got.Message)
	}
}

// An unconfigured Mattermost must change nothing: the register works, the
// trail is written, and no request fails because the chat is missing.
func TestWithoutMattermostEverythingElseStillWorks(t *testing.T) {
	h := newHarness(t)
	h.server.chat = nil // as good as unconfigured for the call below
	h.server.chat = mattermostDisabled()

	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", url.Values{
		"fornamn": {"Bo"}, "efternamn": {"Test"},
		"epost": {"bo@example.test"}, "varfor": {"middagar"},
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d, want it to work without a chat server", rec.Code)
	}
	h.chat.quiet(t)
	if _, err := h.store.OpenCandidateByEmail(context.Background(),
		"bo@example.test", nil, h.cfg.Location()); err != nil {
		t.Errorf("the candidate was not recorded: %v", err)
	}
}
