package sync

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// These are regression tests for a real incident. On its first run against a
// correctly configured Workspace, an empty register set about removing every
// member of the house from bomedlemmar@ — because an empty register looks
// exactly like "nobody should be in this group".
//
// The reconciler talks to Google, so these tests exercise the decision rather
// than the call: the guard has to fire before any client is touched, which is
// why a Syncer with a nil client is enough to prove it. If a guard regresses,
// the nil client panics instead of quietly passing.

func guarded(t *testing.T, yaml string) (*Syncer, *store.Store) {
	t.Helper()
	cfg, err := config.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "guard.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	s := New(cfg, config.Runtime{}, st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.now = func() time.Time { return time.Date(2026, time.June, 1, 12, 0, 0, 0, cfg.Location()) }
	return s, st
}

const guardConfig = `
site: {title: Test, language: sv, timezone: Europe/Stockholm}
membership: {fee_kr: 200}
groups:
  - {kind: bo, email: bomedlemmar@example.test}
  - {kind: van, email: friends@example.test}
contacts:
  accounts: [styrelsen@example.test]
`

// The incident itself: an empty register must not empty a group. The nil
// Google client is the assertion — reaching it at all would panic.
func TestAnEmptyRegisterNeverTouchesAGroup(t *testing.T) {
	s, _ := guarded(t, guardConfig)

	run := s.syncGroup(context.Background(), TriggerStart, s.cfg.Groups[0], nil)

	if run.Removed != 0 || run.Added != 0 {
		t.Errorf("it changed something: +%d −%d", run.Added, run.Removed)
	}
	if run.OK {
		t.Error("it reported success; leaving a group alone because the register is " +
			"empty is a problem the board has to be told about")
	}
	if run.Message == "" {
		t.Error("no explanation was recorded")
	}
}

// Worse in the address books, where a card carries a name and a number that
// may exist nowhere else — and where, on a fresh deployment, the labels are
// the only copy of the membership there is.
func TestAnEmptyRegisterNeverTouchesAnAddressBook(t *testing.T) {
	s, _ := guarded(t, guardConfig)

	run := s.syncContacts(context.Background(), TriggerStart, "styrelsen@example.test",
		map[config.Kind][]store.Member{}, map[string]store.Member{})

	if run.Removed != 0 || run.Added != 0 || run.Updated != 0 {
		t.Errorf("it changed something: +%d −%d ~%d", run.Added, run.Removed, run.Updated)
	}
	if run.OK {
		t.Error("it reported success")
	}
}

// A register holding one member is not much better than an empty one: it
// would still take fifty people out of the group. The brake counts what a run
// is about to remove and refuses the whole lot rather than doing most of it.
func TestTheRemovalBrakeHasASaneDefault(t *testing.T) {
	s, _ := guarded(t, guardConfig)
	if got := s.cfg.Sync.MaxRemovalsPerRun; got != 5 {
		t.Errorf("max_removals_per_run defaults to %d, want 5 — a house loses a few "+
			"members a year, not a dozen in ten minutes", got)
	}
}

func TestTheRemovalBrakeCanBeSetAndTurnedOff(t *testing.T) {
	s, _ := guarded(t, guardConfig+"\nsync: {max_removals_per_run: 25}\n")
	if got := s.cfg.Sync.MaxRemovalsPerRun; got != 25 {
		t.Errorf("got %d, want 25", got)
	}

	off, _ := guarded(t, guardConfig+"\nsync: {max_removals_per_run: -1}\n")
	if got := off.cfg.Sync.MaxRemovalsPerRun; got != 0 {
		t.Errorf("a negative limit should mean off (0), got %d", got)
	}
}

// The guard must not stand in the way of the ordinary case, or somebody will
// turn it off and lose the protection with it.
func TestAPopulatedRegisterIsNotHeldUp(t *testing.T) {
	s, _ := guarded(t, guardConfig)
	loc := s.cfg.Location()
	member := store.Member{
		ID: "m1", FirstName: "Anna", LastName: "Andersson",
		Email: "anna@example.test", Kind: config.KindBo,
		JoinedOn: time.Date(2020, time.January, 1, 0, 0, 0, 0, loc),
	}

	// With a member to sync, the guard lets it through — and then the nil
	// client is reached, which is the proof that it did.
	defer func() {
		if recover() == nil {
			t.Error("the guard blocked a register that has members in it")
		}
	}()
	s.syncGroup(context.Background(), TriggerStart, s.cfg.Groups[0], []store.Member{member})
}

// A bomedlem who has been added to the friends as well belongs in both
// groups — that is the whole feature — but only under their own label in the
// address books, because a contact card says who somebody is.
func TestAnExtraGroupMembershipReachesTheGroupButNotTheAddressBook(t *testing.T) {
	s, _ := guarded(t, guardConfig)
	loc := s.cfg.Location()

	resident := store.Member{
		ID: "m1", FirstName: "Nora", LastName: "Ekwall",
		Email: "nora@example.test", Kind: config.KindBo,
		AlsoIn:   []config.Kind{config.KindVan},
		JoinedOn: time.Date(2020, time.January, 1, 0, 0, 0, 0, loc),
	}

	if !resident.In(config.KindBo) || !resident.In(config.KindVan) {
		t.Fatal("the member should be in both groups")
	}

	// The address-book pass filters to the member's own kind, so a run over
	// the friends label sees nobody and the empty-register guard fires —
	// which is the observable proof that the card is not duplicated.
	run := s.syncContacts(context.Background(), TriggerStart, "styrelsen@example.test",
		map[config.Kind][]store.Member{config.KindVan: {resident}},
		map[string]store.Member{"nora@example.test": resident})
	if run.Added != 0 {
		t.Errorf("a contact card was created under a label that is not the member's kind")
	}
}
