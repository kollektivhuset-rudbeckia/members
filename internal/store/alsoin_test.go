package store

import (
	"context"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// A bomedlem who runs a matlag has to be able to write to the vänmedlemmar,
// and a Google group only takes post from somebody who is in it. Before this
// was a column it was a hand-kept list in the configuration file.
func TestExtraGroupMembershipRoundTrips(t *testing.T) {
	st, ctx := open(t), context.Background()

	m := anna()
	m.AlsoIn = []config.Kind{config.KindVan}
	if err := st.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}

	got, err := st.Member(ctx, "a1", stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AlsoIn) != 1 || got.AlsoIn[0] != config.KindVan {
		t.Fatalf("AlsoIn: got %v, want [van]", got.AlsoIn)
	}

	// The whole point: they are in both groups.
	if !got.In(config.KindBo) || !got.In(config.KindVan) {
		t.Errorf("In(): bo=%v van=%v, want both true", got.In(config.KindBo), got.In(config.KindVan))
	}
	// But only one of them is who they actually are.
	if got.Extra(config.KindBo) {
		t.Error("their own kind should not read as an extra")
	}
	if !got.Extra(config.KindVan) {
		t.Error("the added group should read as an extra")
	}
}

func TestExtraGroupMembershipCanBeTakenAway(t *testing.T) {
	st, ctx := open(t), context.Background()
	m := anna()
	m.AlsoIn = []config.Kind{config.KindVan}
	if err := st.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.AlsoIn = nil
	if err := st.UpdateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Member(ctx, "a1", stockholm)
	if got.In(config.KindVan) {
		t.Error("the extra membership survived being removed")
	}
}

func TestAMemberWithNoExtrasIsOnlyInTheirOwnGroup(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	got, _ := st.Member(ctx, "a1", stockholm)
	if !got.In(config.KindBo) || got.In(config.KindVan) {
		t.Errorf("bo=%v van=%v, want true and false", got.In(config.KindBo), got.In(config.KindVan))
	}
}

// The snapshot has to carry it, or the board would approve a change that
// silently dropped somebody off a mailing list.
func TestASnapshotCarriesTheExtraMemberships(t *testing.T) {
	m := anna()
	m.AlsoIn = []config.Kind{config.KindVan}
	s := SnapshotOf(m)
	if s.AlsoIn != "van" {
		t.Fatalf("snapshot: got %q, want %q", s.AlsoIn, "van")
	}

	back, err := s.Apply(Member{}, stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if !back.In(config.KindVan) {
		t.Error("Apply lost the extra membership")
	}

	// And a Snapshot stays comparable with ==, which is what tells the board
	// that a member changed underneath a waiting proposal.
	if SnapshotOf(m) != s {
		t.Error("two snapshots of one member are not equal")
	}
}

// A register written before the column existed has to keep its members.
func TestAnOlderDatabaseGainsTheColumn(t *testing.T) {
	st, ctx := open(t), context.Background()

	// Drop back to the shape the first release wrote, keeping a member in it.
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `ALTER TABLE members DROP COLUMN also_in`); err != nil {
		t.Skipf("this SQLite cannot drop a column: %v", err)
	}
	if err := migrate(st.db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := st.Member(ctx, "a1", stockholm)
	if err != nil {
		t.Fatalf("the member did not survive the migration: %v", err)
	}
	if len(got.AlsoIn) != 0 {
		t.Errorf("an existing member gained an extra group out of nowhere: %v", got.AlsoIn)
	}
	// And running it again changes nothing.
	if err := migrate(st.db); err != nil {
		t.Fatalf("migrate is not repeatable: %v", err)
	}
}
