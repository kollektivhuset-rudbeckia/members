package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// filled is the public form with everything a person would type.
func filled(extra url.Values) url.Values {
	f := url.Values{
		"fornamn":   {"Åsa"},
		"efternamn": {"Öberg"},
		"epost":     {"asa@example.test"},
	}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

// Bomedlem is not a thing you can ask to be: it follows from moving in, which
// is a separate decision on a separate day. Offering it on a public form
// invited people to apply for something nobody could grant them, so the form
// no longer asks which membership at all.
func TestThePublicFormNoLongerOffersAMembership(t *testing.T) {
	h := newHarness(t)
	body := h.do(t, config.RoleNone, "GET", "/bli-medlem", nil).Body.String()

	if strings.Contains(body, `name="typ"`) {
		t.Error("the form still asks which membership somebody wants")
	}
	if strings.Contains(body, `value="bo"`) {
		t.Error("the form still offers bomedlem as something to pick")
	}
	// It says so in words, too, rather than only leaving the choice out.
	if !strings.Contains(body, "Bomedlem är inget man söker") {
		t.Errorf("the page does not say bomedlem cannot be applied for")
	}
	// And it does ask why.
	if !strings.Contains(body, `name="varfor"`) {
		t.Error("the form does not ask why somebody wants to join")
	}
}

// Whatever anybody posts, they become a vänmedlem. A hand-made request asking
// for bomedlem must not be a way around the house's own decision.
func TestEverybodyWhoAppliesBecomesAFriendMember(t *testing.T) {
	h := newHarness(t)

	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem",
		filled(url.Values{"varfor": {"flytta-in"}, "typ": {"bo"}}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d, want a redirect", rec.Code)
	}

	c, err := h.store.OpenCandidateByEmail(context.Background(),
		"asa@example.test", nil, h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if c.Kind != config.KindVan {
		t.Errorf("kind: got %q, want van whatever was posted", c.Kind)
	}
	// Wanting to move in is recorded as a reason, which is what it is.
	if !slices.Equal(c.Reasons, []string{"flytta-in"}) {
		t.Errorf("reasons: got %q", c.Reasons)
	}
}

// The reason is the point of asking, so the form insists on one — and only on
// one it actually offered.
func TestTheFormInsistsOnAReasonItOffered(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
		want int
	}{
		{"no reason at all", filled(nil), http.StatusUnprocessableEntity},
		{"a reason nobody was offered", filled(url.Values{"varfor": {"pokerklubben"}}),
			http.StatusUnprocessableEntity},
		{"something else with nothing said", filled(url.Values{"varfor": {"other"}}),
			http.StatusUnprocessableEntity},
		{"a real reason", filled(url.Values{"varfor": {"matlag"}}), http.StatusSeeOther},
		{"one offered and one not", filled(url.Values{"varfor": {"matlag", "pokerklubben"}}),
			http.StatusUnprocessableEntity},
		{"something else among others, with nothing said", filled(url.Values{
			"varfor": {"matlag", "other"},
		}), http.StatusUnprocessableEntity},
		{"something else, said", filled(url.Values{
			"varfor": {"other"}, "varfor_annat": {"jag är nyfiken på snickarboden"},
		}), http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", tc.form)
			if rec.Code != tc.want {
				t.Errorf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// People rarely come for one thing only, so the form takes as many reasons as
// apply, and the card and the chat message show every one of them in the
// order the form lists them, with their own words last.
func TestSeveralReasonsAreKeptAndShown(t *testing.T) {
	h := newHarness(t)
	const said = "snickarboden"

	if rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", filled(url.Values{
		"varfor": {"other", "karaoke", "middagar", "karaoke"}, "varfor_annat": {said},
	})); rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}
	c, err := h.store.OpenCandidateByEmail(context.Background(),
		"asa@example.test", nil, h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"other", "karaoke", "middagar"}; !slices.Equal(c.Reasons, want) {
		t.Errorf("stored %q, want %q", c.Reasons, want)
	}
	if c.ReasonNote != said {
		t.Errorf("note: got %q", c.ReasonNote)
	}

	const words = "Gemensamma middagar, Karaoke, " + said
	body := h.do(t, config.RoleIntake, "GET", "/kandidater", nil).Body.String()
	if !strings.Contains(body, words) {
		t.Errorf("the card does not show %q", words)
	}
	h.chat.waitFor(t, 1)
	if got := h.chat.sent()[0]; !strings.Contains(got.Message, words) {
		t.Errorf("the notification does not show %q:\n%s", words, got.Message)
	}
}

// Coming back after an error, every box they ticked is still ticked.
func TestTheFormKeepsEveryTickAfterAnError(t *testing.T) {
	h := newHarness(t)
	f := filled(url.Values{"varfor": {"matlag", "odling"}})
	f.Del("epost")
	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", f)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("the form: got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, id := range []string{"matlag", "odling"} {
		if !strings.Contains(body, `value="`+id+`"
					       checked`) {
			t.Errorf("%s is no longer ticked", id)
		}
	}
	if strings.Contains(body, `value="karaoke"
					       checked`) {
		t.Error("a box nobody ticked came back ticked")
	}
}

// "Something else" keeps the person's own words, because the whole reason it
// exists is that a list of set answers would turn their actual reason into
// the nearest wrong one.
func TestSomethingElseKeepsTheirOwnWords(t *testing.T) {
	h := newHarness(t)
	const said = "jag är nyfiken på snickarboden"

	if rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", filled(url.Values{
		"varfor": {"other"}, "varfor_annat": {said},
	})); rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}

	c, err := h.store.OpenCandidateByEmail(context.Background(),
		"asa@example.test", nil, h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.Reasons, []string{config.ReasonOther}) || c.ReasonNote != said {
		t.Errorf("stored %q / %q, want %q / %q", c.Reasons, c.ReasonNote, config.ReasonOther, said)
	}

	// The interview team sees the words, not the id.
	h.chat.waitFor(t, 1)
	if got := h.chat.sent()[0]; !strings.Contains(got.Message, said) {
		t.Errorf("the notification does not carry what they wrote:\n%s", got.Message)
	}
}

// A note typed and then abandoned is not an answer: picking a set reason
// afterwards must not leave the stray text behind on the card.
func TestANoteIsDroppedWhenASetReasonIsPicked(t *testing.T) {
	h := newHarness(t)

	if rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", filled(url.Values{
		"varfor": {"karaoke"}, "varfor_annat": {"ändrade mig"},
	})); rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}
	c, err := h.store.OpenCandidateByEmail(context.Background(),
		"asa@example.test", nil, h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if c.ReasonNote != "" {
		t.Errorf("an abandoned note was kept: %q", c.ReasonNote)
	}
}

// The reason is stored as an id and resolved to words when shown, so that
// renaming one renames it everywhere rather than leaving two generations of
// wording in the register.
func TestRenamingAReasonRenamesItOnTheCard(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(t, config.RoleNone, "POST", "/bli-medlem",
		filled(url.Values{"varfor": {"matlag"}})); rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}

	body := h.do(t, config.RoleIntake, "GET", "/kandidater", nil).Body.String()
	if !strings.Contains(body, "Matlaget") {
		t.Fatalf("the board does not show the reason:\n%s", firstLines(body, 0))
	}

	// Rename it in the configuration; the card follows.
	for i := range h.cfg.Join.Reasons {
		if h.cfg.Join.Reasons[i].ID == "matlag" {
			h.cfg.Join.Reasons[i].Name = "Matlagen"
		}
	}
	body = h.do(t, config.RoleIntake, "GET", "/kandidater", nil).Body.String()
	if !strings.Contains(body, "Matlagen") {
		t.Error("the card kept the old wording after the reason was renamed")
	}
}

// A reason taken out of the configuration entirely must not empty the row on
// somebody's card: they did answer, and the answer is still on record.
func TestAReasonRemovedFromTheConfigurationStillShows(t *testing.T) {
	h := newHarness(t)
	if rec := h.do(t, config.RoleNone, "POST", "/bli-medlem",
		filled(url.Values{"varfor": {"karaoke"}})); rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}
	h.cfg.Join.Reasons = nil

	body := h.do(t, config.RoleIntake, "GET", "/kandidater", nil).Body.String()
	if !strings.Contains(body, "karaoke") {
		t.Error("the card silently lost an answer whose reason was removed")
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if n <= 0 || n > len(lines) {
		n = 12
	}
	return strings.Join(lines[:n], "\n")
}

// thanksFor sends the form with these reasons and returns the page it leads to.
func thanksFor(t *testing.T, h *harness, reasons ...string) string {
	t.Helper()
	rec := h.do(t, config.RoleNone, "POST", "/bli-medlem", filled(url.Values{"varfor": reasons}))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("the form: got %d", rec.Code)
	}
	return h.do(t, config.RoleNone, "GET", rec.Header().Get("Location"), nil).Body.String()
}

// The page is for the curious. Only somebody who said they want to become a
// member, or to move in, is shown the fee; everybody else is thanked and told
// somebody will be in touch.
func TestOnlyThoseWhoWantToJoinAreAskedToPay(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reasons []string
		pay     bool
	}{
		{"curious about the dinners", []string{"middagar", "karaoke"}, false},
		{"a friend member", []string{"vanmedlem"}, true},
		{"moving in", []string{"flytta-in"}, true},
		{"curious, and a friend member too", []string{"odling", "vanmedlem"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t)
			body := thanksFor(t, h, tc.reasons...)
			if got := strings.Contains(body, "Betala årsavgiften"); got != tc.pay {
				t.Errorf("asked to pay: got %v, want %v", got, tc.pay)
			}
			if got := strings.Contains(body, "Tack för ditt intresse"); got == tc.pay {
				t.Errorf("thanked for the interest: got %v, want %v", got, !tc.pay)
			}
		})
	}
}

// Which reasons mean joining is the house's to say, like the reasons
// themselves.
func TestWhichReasonsMeanJoiningIsConfiguration(t *testing.T) {
	h := newHarness(t)
	for i := range h.cfg.Join.Reasons {
		if h.cfg.Join.Reasons[i].ID == "matlag" {
			h.cfg.Join.Reasons[i].Joins = true
		}
	}
	if body := thanksFor(t, h, "matlag"); !strings.Contains(body, "Betala årsavgiften") {
		t.Error("a reason configured to mean joining did not lead to the fee")
	}
}
