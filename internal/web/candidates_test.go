package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// The board belongs to the interview team. Everybody else who can reach it at
// all reads it: the pipeline is ny@'s process and ny@'s judgement about who is
// where, and somebody else quietly moving a card out of "interview booked" is
// exactly the confusion this separation exists to prevent.
//
// The router is what has to hold this. A control hidden in a template is a
// courtesy to the reader, not a permission.
func TestOnlyTheInterviewTeamMayWorkTheBoard(t *testing.T) {
	for _, role := range []config.Role{config.RoleBoard, config.RoleCashier} {
		t.Run(string(role), func(t *testing.T) {
			h := newHarness(t)
			id := h.candidate(t, "Bo", "bo@example.test", "new")

			for _, tc := range []struct {
				what   string
				method string
				path   string
				form   url.Values
			}{
				{"move a card", "POST", "/kandidater/" + id, url.Values{"steg": {"limbo"}}},
				{"edit a field", "POST", "/kandidater/" + id + "/falt",
					url.Values{"falt": {"ansvarig"}, "varde": {"Någon"}}},
				{"welcome somebody", "POST", "/kandidater/" + id + "/valkomna", url.Values{}},
				{"delete a card", "POST", "/kandidater/" + id + "/ta-bort", url.Values{}},
				{"clear a column", "POST", "/kandidater/rensa", url.Values{"steg": {"welcomed"}}},
				{"add a card", "POST", "/kandidater/ny", url.Values{"fornamn": {"Ny"}}},
				{"open the add page", "GET", "/kandidater/ny", nil},
			} {
				rec := h.do(t, role, tc.method, tc.path, tc.form)
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s may %s: got %d, want 403", role, tc.what, rec.Code)
				}
			}

			// The card is untouched by all of that.
			c, err := h.store.Candidate(context.Background(), id, h.cfg.Location())
			if err != nil {
				t.Fatal(err)
			}
			if c.Stage != "new" {
				t.Errorf("the card moved to %q anyway", c.Stage)
			}
		})
	}
}

// Seeing it is a different matter: the board may read the pipeline, and the
// cashier has no business in it at all.
func TestWhoMayLookAtTheBoard(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct {
		role config.Role
		want int
	}{
		{config.RoleIntake, http.StatusOK},
		{config.RoleBoard, http.StatusOK},
		{config.RoleCashier, http.StatusForbidden},
	} {
		if rec := h.do(t, tc.role, "GET", "/kandidater", nil); rec.Code != tc.want {
			t.Errorf("%s reading the board: got %d, want %d", tc.role, rec.Code, tc.want)
		}
	}
}

// A reader gets no controls that would fail if they used them.
func TestTheBoardIsShownReadOnlyToTheBoard(t *testing.T) {
	h := newHarness(t)
	h.candidate(t, "Bo", "bo@example.test", "new")

	body := h.do(t, config.RoleBoard, "GET", "/kandidater", nil).Body.String()
	for _, unwanted := range []string{
		"/ta-bort", "/kandidater/rensa", "/valkomna", "data-edit=", "draggable=", "data-board",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("the read-only board still offers %q", unwanted)
		}
	}

	// And the team's own view does have them.
	body = h.do(t, config.RoleIntake, "GET", "/kandidater", nil).Body.String()
	for _, wanted := range []string{"/ta-bort", "data-edit=", "draggable="} {
		if !strings.Contains(body, wanted) {
			t.Errorf("the team's board is missing %q", wanted)
		}
	}
}

// Deleting a card must never reach the register. Somebody who was welcomed is
// a member; the card is only the trail that says how they arrived, and the
// team clearing their finished columns is not asking to remove members.
func TestDeletingACardLeavesTheMemberAlone(t *testing.T) {
	h := newHarness(t)
	id := h.candidate(t, "Vera", "vera@example.test", "new")

	rec := h.do(t, config.RoleIntake, "POST", "/kandidater/"+id+"/valkomna", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("welcoming: got %d, want a redirect", rec.Code)
	}
	m, err := h.store.MemberByEmail(context.Background(), "vera@example.test", h.cfg.Location())
	if err != nil {
		t.Fatalf("welcoming did not make a member: %v", err)
	}

	rec = h.do(t, config.RoleIntake, "POST", "/kandidater/"+id+"/ta-bort", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("deleting: got %d, want a redirect", rec.Code)
	}
	if _, err := h.store.Candidate(context.Background(), id, h.cfg.Location()); err == nil {
		t.Error("the card is still on the board")
	}
	if _, err := h.store.MemberByEmail(context.Background(), "vera@example.test", h.cfg.Location()); err != nil {
		t.Errorf("deleting the card took the member with it: %v", err)
	}
	_ = m
}

func TestClearingAFinishedColumnEmptiesItAndSaysWho(t *testing.T) {
	h := newHarness(t)
	h.candidate(t, "Ann", "ann@example.test", "rejected")
	h.candidate(t, "Bea", "bea@example.test", "rejected")
	stays := h.candidate(t, "Cim", "cim@example.test", "new")

	rec := h.do(t, config.RoleIntake, "POST", "/kandidater/rensa",
		url.Values{"steg": {"rejected"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("clearing: got %d, want a redirect", rec.Code)
	}

	list, err := h.store.Candidates(context.Background(), h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != stays {
		t.Errorf("the board holds %d cards, want only the open one", len(list))
	}

	// The names are the only part of a deleted card worth keeping, so they go
	// in the trail — one line each, not one line saying "2 cards".
	entries, err := h.store.Audit(context.Background(), 20)
	if err != nil {
		t.Fatal(err)
	}
	var deleted []string
	for _, e := range entries {
		if e.Action == "candidate.deleted" {
			deleted = append(deleted, e.Subject)
		}
	}
	if len(deleted) != 2 {
		t.Fatalf("the trail records %d deletions, want 2: %v", len(deleted), deleted)
	}
	for _, want := range []string{"ann@example.test", "bea@example.test"} {
		if !strings.Contains(strings.Join(deleted, " "), want) {
			t.Errorf("the trail does not say who: %v, missing %s", deleted, want)
		}
	}
}

// An open column holds people who sent the form and are waiting to hear from
// us. Losing them silently is the worst thing this register can do to a
// person, so the refusal lives in the handler and not only in the template.
func TestAnOpenColumnCannotBeCleared(t *testing.T) {
	h := newHarness(t)
	h.candidate(t, "Ann", "ann@example.test", "new")
	h.candidate(t, "Bea", "bea@example.test", "interview")

	for _, stage := range []string{"new", "interview", "limbo"} {
		rec := h.do(t, config.RoleIntake, "POST", "/kandidater/rensa",
			url.Values{"steg": {stage}})
		if rec.Code != http.StatusSeeOther {
			t.Errorf("clearing %q: got %d, want a redirect carrying the refusal", stage, rec.Code)
		}
	}

	list, err := h.store.Candidates(context.Background(), h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("an open column was cleared anyway: %d cards left of 2", len(list))
	}
}

// A stage that is not in the configuration at all is refused rather than
// silently matching nothing.
func TestClearingRefusesAStageThatDoesNotExist(t *testing.T) {
	h := newHarness(t)
	h.candidate(t, "Ann", "ann@example.test", "rejected")

	rec := h.do(t, config.RoleIntake, "POST", "/kandidater/rensa",
		url.Values{"steg": {"made-up"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("got %d, want a redirect", rec.Code)
	}
	n, err := h.store.CountCandidates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the board holds %d cards, want 1", n)
	}
}

// The store call itself does not decide what may be cleared — that is the
// handler's business — but it must report what it removed, because the caller
// writes those names down.
func TestClearCandidateStageReturnsWhatItRemoved(t *testing.T) {
	h := newHarness(t)
	h.candidate(t, "Ann", "ann@example.test", "welcomed")
	h.candidate(t, "Bea", "bea@example.test", "welcomed")

	gone, err := h.store.ClearCandidateStage(context.Background(), "welcomed", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 {
		t.Fatalf("returned %d cards, want 2", len(gone))
	}
	for _, c := range gone {
		if c.Name() == "" || c.Email == "" {
			t.Errorf("a returned card is missing its details: %+v", c)
		}
	}

	// Clearing an already empty stage is not an error, and removes nothing.
	gone, err = h.store.ClearCandidateStage(context.Background(), "welcomed", h.cfg.Location())
	if err != nil {
		t.Fatalf("clearing an empty stage: %v", err)
	}
	if len(gone) != 0 {
		t.Errorf("returned %d cards from an empty stage", len(gone))
	}
}
