package web

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
)

// Every imported member shares one joined date, so correcting it afterwards
// is the normal case, not an edge one: ny@ proposes, the board applies.
func TestCorrectingAnImportedJoinedDate(t *testing.T) {
	h := newHarness(t)
	h.member(t, "m1", "anna@example.test", config.KindBo)
	ctx := context.Background()

	form := url.Values{
		"fornamn": {"Anna"}, "efternamn": {"Andersson"},
		"epost": {"anna@example.test"}, "typ": {"bo"},
		"medlem_sedan": {"2014-04-01"},
		"anledning":    {"Anna har varit med sedan huset byggdes"},
	}

	// ny@ cannot change it outright.
	if rec := h.do(t, config.RoleIntake, "POST", "/medlem/m1", form); rec.Code != http.StatusSeeOther {
		t.Fatalf("intake edit: got %d", rec.Code)
	}
	m, _ := h.store.Member(ctx, "m1", h.cfg.Location())
	if m.JoinedOn.Year() == 2014 {
		t.Fatal("intake changed the date without the board")
	}
	pending, _ := h.store.PendingProposals(ctx)
	if len(pending) != 1 || pending[0].After.JoinedOn != "2014-04-01" {
		t.Fatalf("the proposal did not carry the new date: %+v", pending)
	}

	// The board applies it.
	h.do(t, config.RoleBoard, "POST", "/andringar/"+pending[0].ID+"/godkann", url.Values{})
	m, _ = h.store.Member(ctx, "m1", h.cfg.Location())
	if i18n.ISODate(m.JoinedOn.In(h.cfg.Location())) != "2014-04-01" {
		t.Errorf("after approval: got %s, want 2014-04-01", m.JoinedOn)
	}

	// And the cashier may set one directly, no proposal needed.
	form.Set("medlem_sedan", "2015-05-05")
	h.do(t, config.RoleCashier, "POST", "/medlem/m1", form)
	m, _ = h.store.Member(ctx, "m1", h.cfg.Location())
	if i18n.ISODate(m.JoinedOn.In(h.cfg.Location())) != "2015-05-05" {
		t.Errorf("cashier edit: got %s, want 2015-05-05", m.JoinedOn)
	}
}
