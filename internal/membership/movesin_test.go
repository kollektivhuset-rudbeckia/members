package membership

import (
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// The note a friend member carries is the interview team's working context.
// It earns its place while somebody is waiting and stops earning it the day
// they move in — but only then. A note written about a neighbour afterwards
// is somebody deliberately writing something down, and the rule leaves it be.
func TestTheNoteGoesOnlyOnMovingIn(t *testing.T) {
	note := "vill ha 3:a, träffade dem på öppet hus"
	for _, tc := range []struct {
		name       string
		from, to   config.Kind
		note       string
		wantNote   string
		wantMoveIn bool
	}{
		{"a friend member moves in", config.KindVan, config.KindBo, note, "", true},
		{"a friend member stays a friend", config.KindVan, config.KindVan, note, note, false},
		{"a resident is edited for something else", config.KindBo, config.KindBo, note, note, false},
		{"a resident becomes a friend member again", config.KindBo, config.KindVan, note, note, false},
		{"moving in with no note is no drama", config.KindVan, config.KindBo, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := store.Member{ID: "m1", Kind: tc.from, Note: tc.note}
			after := store.Member{ID: "m1", Kind: tc.to, Note: tc.note}

			if got := MovesIn(before, after); got != tc.wantMoveIn {
				t.Errorf("MovesIn: got %v, want %v", got, tc.wantMoveIn)
			}
			if got := OnMovesIn(before, after); got.Note != tc.wantNote {
				t.Errorf("note: got %q, want %q", got.Note, tc.wantNote)
			}
		})
	}
}

// A note typed in the same edit that moves somebody in still goes. The rule
// is about the transition, and somebody filling in a flat number and leaving
// the interview note in the box has not decided to keep it.
func TestMovingInDropsEvenANoteSuppliedWithTheChange(t *testing.T) {
	before := store.Member{ID: "m1", Kind: config.KindVan, Note: "gammal anteckning"}
	after := store.Member{ID: "m1", Kind: config.KindBo, Note: "något nytt"}
	if got := OnMovesIn(before, after).Note; got != "" {
		t.Errorf("note: got %q, want it dropped", got)
	}
}

// Nothing else about the member is touched.
func TestOnMovesInChangesNothingButTheNote(t *testing.T) {
	before := store.Member{ID: "m1", Kind: config.KindVan}
	after := store.Member{
		ID: "m1", FirstName: "Åsa", LastName: "Öberg", Email: "asa@example.test",
		Phone: "070", Kind: config.KindBo, Apartment: "42", Note: "borta",
	}
	got := OnMovesIn(before, after)
	after.Note = ""
	if got.Name() != after.Name() || got.Email != after.Email ||
		got.Phone != after.Phone || got.Apartment != after.Apartment ||
		got.Kind != after.Kind {
		t.Errorf("something other than the note changed:\n got  %+v\n want %+v", got, after)
	}
}
