package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// noted puts a friend member in the register carrying an interview note.
func (h *harness) noted(t *testing.T, id, email, note string) store.Member {
	t.Helper()
	m := store.Member{
		ID: id, FirstName: "Åsa", LastName: "Öberg", Email: email,
		Kind: config.KindVan, Note: note, JoinedOn: h.now.AddDate(-1, 0, 0),
		CreatedAt: h.now.AddDate(-1, 0, 0), UpdatedAt: h.now.AddDate(-1, 0, 0),
	}
	if err := h.store.CreateMember(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	return m
}

// editForm is the whole member form, which the save handler expects: a
// partial post would read as somebody clearing the fields it left out.
func editForm(m store.Member, kind config.Kind, note, joined string) url.Values {
	return url.Values{
		"fornamn":      {m.FirstName},
		"efternamn":    {m.LastName},
		"epost":        {m.Email},
		"telefon":      {m.Phone},
		"typ":          {string(kind)},
		"lagenhet":     {m.Apartment},
		"medlem_sedan": {joined},
		"anteckning":   {note},
	}
}

func TestMovingInDropsTheNoteThroughTheEditForm(t *testing.T) {
	h := newHarness(t)
	m := h.noted(t, "m1", "asa@example.test", "vill ha 3:a, trevlig")

	rec := h.do(t, config.RoleBoard, "POST", "/medlem/m1",
		editForm(m, config.KindBo, m.Note, "2025-06-01"))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("saving: got %d, want a redirect", rec.Code)
	}

	got, err := h.store.Member(context.Background(), "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != config.KindBo {
		t.Fatalf("kind: got %q, want bo", got.Kind)
	}
	if got.Note != "" {
		t.Errorf("note: got %q, want it dropped on moving in", got.Note)
	}
}

// The other direction of the same rule: editing a resident must not quietly
// wipe a note somebody wrote about a neighbour.
func TestEditingAResidentKeepsTheirNote(t *testing.T) {
	h := newHarness(t)
	m := h.noted(t, "m1", "asa@example.test", "")
	m.Kind = config.KindBo
	m.Note = "har husets andra uppsättning nycklar"
	if err := h.store.UpdateMember(context.Background(), m); err != nil {
		t.Fatal(err)
	}

	form := editForm(m, config.KindBo, m.Note, "2025-06-01")
	form.Set("telefon", "070-000 00 00")
	if rec := h.do(t, config.RoleBoard, "POST", "/medlem/m1", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("saving: got %d, want a redirect", rec.Code)
	}

	got, err := h.store.Member(context.Background(), "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if got.Note != m.Note {
		t.Errorf("note: got %q, want it kept: %q", got.Note, m.Note)
	}
}

// ny@ cannot edit outright, so the same change arrives as a proposal. It must
// land with the note gone, and the board must be able to see that is what
// they are approving rather than discovering it afterwards.
func TestMovingInDropsTheNoteThroughAProposal(t *testing.T) {
	h := newHarness(t)
	m := h.noted(t, "m1", "asa@example.test", "vill ha 3:a, trevlig")

	form := editForm(m, config.KindBo, m.Note, "2025-06-01")
	form.Set("anledning", "flyttar in i 42")
	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("proposing: got %d, want a redirect", rec.Code)
	}

	list, err := h.store.ProposalsFor(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d proposals, want 1", len(list))
	}
	// Filed for what would actually happen, not for a version with the note
	// still in it.
	if list[0].After.Note != "" {
		t.Errorf("the proposal still carries the note: %q", list[0].After.Note)
	}

	rec := h.do(t, config.RoleBoard, "POST", "/andringar/"+list[0].ID+"/godkann", url.Values{})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("approving: got %d, want a redirect", rec.Code)
	}
	got, err := h.store.Member(context.Background(), "m1", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != config.KindBo {
		t.Fatalf("kind: got %q, want bo", got.Kind)
	}
	if got.Note != "" {
		t.Errorf("note: got %q, want it dropped", got.Note)
	}
}

// Welcoming somebody is the association agreeing to know them, not handing
// them a flat. They arrive as a friend member and keep the note that got them
// there; bomedlem is a separate decision on a later day.
func TestWelcomingMakesAFriendMemberAndKeepsTheNote(t *testing.T) {
	h := newHarness(t)
	id := h.candidate(t, "Vera", "vera@example.test", "new")

	c, err := h.store.Candidate(context.Background(), id, h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	c.Note = "vill flytta in, intervjuad i maj"
	// Even a candidate who asked to be a resident becomes a friend member.
	c.Kind = config.KindBo
	if err := h.store.UpdateCandidate(context.Background(), c); err != nil {
		t.Fatal(err)
	}

	if rec := h.do(t, config.RoleIntake, "POST", "/kandidater/"+id+"/valkomna",
		url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("welcoming: got %d, want a redirect", rec.Code)
	}

	m, err := h.store.MemberByEmail(context.Background(), "vera@example.test", h.cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	if m.Kind != config.KindVan {
		t.Errorf("kind: got %q, want van — welcoming does not hand out a flat", m.Kind)
	}
	if m.Note != c.Note {
		t.Errorf("note: got %q, want the candidate's note kept: %q", m.Note, c.Note)
	}
}
