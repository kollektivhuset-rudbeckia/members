package demo

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

func setup(t *testing.T) (*store.Store, *config.Config, time.Time) {
	t.Helper()
	cfg, err := config.Parse([]byte(`
site: {title: Test, language: sv, timezone: Europe/Stockholm}
membership: {fee_kr: 200, due_on: "03-31", grace_days: 14, new_member_days: 45}
groups:
  - {kind: bo, email: bo@example.test}
  - {kind: van, email: van@example.test}
`))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "demo.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, cfg, time.Date(2026, time.September, 5, 12, 0, 0, 0, cfg.Location())
}

func TestTheDemoSeedsAHouseThatLooksLivedIn(t *testing.T) {
	st, cfg, now := setup(t)
	ctx := context.Background()

	n, err := Seed(ctx, st, cfg, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n < 20 {
		t.Errorf("got %d members; a demo wants enough to scroll", n)
	}

	members, err := st.Members(ctx, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	payments, err := st.AllPayments(ctx, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	counts := membership.Build(members, payments, cfg, now).Count()

	// The point of the demo is that every state is on screen without anybody
	// having to make one happen.
	if counts.Bo == 0 || counts.Van == 0 {
		t.Errorf("both kinds should be represented: %d bo, %d van", counts.Bo, counts.Van)
	}
	if counts.Paid == 0 {
		t.Error("nobody has paid; the register would look broken")
	}
	if counts.Overdue == 0 {
		t.Error("nobody is overdue; the whole chasing half of the register is invisible")
	}
	if counts.Former == 0 {
		t.Error("no former members; the tenure column has nothing interesting in it")
	}

	pending, err := st.PendingProposals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) < 2 {
		t.Errorf("got %d proposals waiting; the approval queue should not be an empty page", len(pending))
	}
}

// Restarting a demo must not pile a second cast on top of the first.
func TestSeedingTwiceChangesNothing(t *testing.T) {
	st, cfg, now := setup(t)
	ctx := context.Background()

	first, err := Seed(ctx, st, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Seed(ctx, st, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Errorf("the second run added %d members", second)
	}
	total, err := st.CountMembers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if total != first {
		t.Errorf("got %d members after two runs, want %d", total, first)
	}
}

// Every address is on example.test and nothing reaches Google, but the ids go
// in URLs, so they have to be plain.
func TestTheDemoCastIsInventedAndItsIdsAreASCII(t *testing.T) {
	st, cfg, now := setup(t)
	ctx := context.Background()
	if _, err := Seed(ctx, st, cfg, now); err != nil {
		t.Fatal(err)
	}
	members, err := st.Members(ctx, cfg.Location())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range members {
		for _, r := range m.ID {
			if r > 127 {
				t.Errorf("%s has a non-ASCII id %q", m.Name(), m.ID)
				break
			}
		}
		if !endsWith(m.Email, "@example.test") {
			t.Errorf("%s has the address %q, which is not obviously invented", m.Name(), m.Email)
		}
	}
}

func endsWith(s, suffix string) bool {
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}
