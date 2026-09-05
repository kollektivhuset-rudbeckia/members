package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

var stockholm = mustLoad()

func mustLoad() *time.Location {
	loc, err := time.LoadLocation("Europe/Stockholm")
	if err != nil {
		panic(err)
	}
	return loc
}

func open(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func onDay(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, stockholm)
}

func anna() Member {
	return Member{
		ID: "a1", FirstName: "Anna", LastName: "Andersson",
		Email: "Anna.Andersson@Example.TEST", Phone: "070-1", Kind: config.KindBo,
		Apartment: "1403", JoinedOn: onDay(2020, time.March, 1),
		CreatedAt: onDay(2020, time.March, 1), CreatedBy: "ny@rudbeckia.nu",
		UpdatedAt: onDay(2020, time.March, 1), UpdatedBy: "ny@rudbeckia.nu",
	}
}

func TestCreateAndReadAMember(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := st.Member(ctx, "a1", stockholm)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The address is the key everything synchronises on, so it is stored in
	// one form however it was typed.
	if got.Email != "anna.andersson@example.test" {
		t.Errorf("email: got %q, want it lowercased", got.Email)
	}
	if got.Name() != "Anna Andersson" {
		t.Errorf("name: got %q", got.Name())
	}
	if !got.Current() {
		t.Error("a member with no leaving date should be current")
	}
	// A day is a day in a place: read back in Stockholm it is still the 1st.
	if got.JoinedOn.Day() != 1 || got.JoinedOn.Month() != time.March {
		t.Errorf("joined on: got %s, want 1 March", got.JoinedOn)
	}
}

func TestTheSameAddressCannotBeAddedTwice(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	second := anna()
	second.ID, second.FirstName = "a2", "Annika"
	// Different case, same mailbox.
	second.Email = "ANNA.ANDERSSON@example.test"

	err := st.CreateMember(ctx, second)
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Fatalf("got %v, want ErrDuplicateEmail", err)
	}
}

func TestUpdateCannotStealAnotherMembersAddress(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	bo := anna()
	bo.ID, bo.FirstName, bo.Email = "b1", "Bo", "bo@example.test"
	if err := st.CreateMember(ctx, bo); err != nil {
		t.Fatal(err)
	}

	bo.Email = "anna.andersson@example.test"
	if err := st.UpdateMember(ctx, bo); !errors.Is(err, ErrDuplicateEmail) {
		t.Fatalf("got %v, want ErrDuplicateEmail", err)
	}
}

func TestLeavingIsRecordedAndReadBack(t *testing.T) {
	st, ctx := open(t), context.Background()
	m := anna()
	if err := st.CreateMember(ctx, m); err != nil {
		t.Fatal(err)
	}
	m.LeftOn = sql.NullTime{Time: onDay(2026, time.June, 30), Valid: true}
	if err := st.UpdateMember(ctx, m); err != nil {
		t.Fatal(err)
	}

	got, err := st.Member(ctx, "a1", stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if got.Current() {
		t.Error("a member with a leaving date should not be current")
	}
	if !got.LeftOn.Valid || got.LeftOn.Time.Day() != 30 {
		t.Errorf("left on: got %v", got.LeftOn)
	}
}

func TestMissingRecordsSaySo(t *testing.T) {
	st, ctx := open(t), context.Background()
	if _, err := st.Member(ctx, "nobody", stockholm); !errors.Is(err, ErrNotFound) {
		t.Errorf("member: got %v, want ErrNotFound", err)
	}
	if _, err := st.Proposal(ctx, "nothing"); !errors.Is(err, ErrNotFound) {
		t.Errorf("proposal: got %v, want ErrNotFound", err)
	}
	if err := st.DeleteMember(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete: got %v, want ErrNotFound", err)
	}
}

func TestMembersComeBackSurnameFirst(t *testing.T) {
	st, ctx := open(t), context.Background()
	for _, m := range []Member{
		{ID: "1", FirstName: "Bo", LastName: "Ek", Email: "b@x.test", Kind: config.KindBo, JoinedOn: onDay(2020, 1, 1)},
		{ID: "2", FirstName: "Anna", LastName: "Ek", Email: "a@x.test", Kind: config.KindBo, JoinedOn: onDay(2020, 1, 1)},
		{ID: "3", FirstName: "Cecilia", LastName: "Dahl", Email: "c@x.test", Kind: config.KindVan, JoinedOn: onDay(2020, 1, 1)},
	} {
		m.CreatedAt, m.UpdatedAt = onDay(2020, 1, 1), onDay(2020, 1, 1)
		if err := st.CreateMember(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.Members(ctx, stockholm)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Cecilia", "Anna", "Bo"} // Dahl, then Ek Anna, then Ek Bo
	for i, w := range want {
		if got[i].FirstName != w {
			t.Errorf("row %d: got %s, want %s", i, got[i].FirstName, w)
		}
	}
}

// --- payments ---------------------------------------------------------------

func TestAYearsFeeIsCorrectedRatherThanDuplicated(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	pay := Payment{MemberID: "a1", Year: 2026, AmountKr: 100,
		PaidOn: onDay(2026, time.February, 1), Method: "Bankgiro",
		RegisteredBy: "ekonomi@rudbeckia.nu", RegisteredAt: onDay(2026, time.February, 2)}
	if err := st.RecordPayment(ctx, pay); err != nil {
		t.Fatal(err)
	}
	// The rest of the fee arrives later; the cashier corrects the amount.
	pay.AmountKr = 200
	if err := st.RecordPayment(ctx, pay); err != nil {
		t.Fatal(err)
	}

	list, err := st.PaymentsFor(ctx, "a1", stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d payments for one year, want 1", len(list))
	}
	if list[0].AmountKr != 200 {
		t.Errorf("amount: got %d, want 200", list[0].AmountKr)
	}
}

func TestWithdrawingAFeeThatIsNotThereSaysSo(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	if err := st.WithdrawPayment(ctx, "a1", 2026); !errors.Is(err, ErrNotFound) {
		t.Errorf("got %v, want ErrNotFound", err)
	}
}

// Removing a member takes their fees with them, or the payments table grows
// rows nobody can attribute to anybody.
func TestRemovingAMemberTakesTheirFees(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordPayment(ctx, Payment{MemberID: "a1", Year: 2026, AmountKr: 200,
		PaidOn: onDay(2026, 2, 1), RegisteredAt: onDay(2026, 2, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMember(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	all, err := st.AllPayments(ctx, stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("payments left behind: %v", all)
	}
}

// --- proposals --------------------------------------------------------------

func proposal(id string) Proposal {
	before := SnapshotOf(anna())
	after := before
	after.Phone = "070-9"
	return Proposal{
		ID: id, MemberID: "a1", Kind: ProposeUpdate, Before: before, After: after,
		Reason: "nytt nummer", Status: Pending,
		ProposedBy: "ny@rudbeckia.nu", ProposedAt: onDay(2026, time.June, 1),
	}
}

func TestAProposalIsDecidedOnlyOnce(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProposal(ctx, proposal("p1")); err != nil {
		t.Fatal(err)
	}

	now := onDay(2026, time.June, 2)
	if err := st.DecideProposal(ctx, "p1", Approved, "styrelsen@rudbeckia.nu", "ok", now); err != nil {
		t.Fatalf("first decision: %v", err)
	}
	// Two board members clicking at once must not both win.
	err := st.DecideProposal(ctx, "p1", Rejected, "styrelsen@rudbeckia.nu", "nej", now)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("second decision: got %v, want ErrNotFound", err)
	}

	got, err := st.Proposal(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Approved {
		t.Errorf("status: got %q, want approved", got.Status)
	}
	if got.DecisionNote != "ok" {
		t.Errorf("the first decision's note should stand: got %q", got.DecisionNote)
	}
}

func TestASnapshotSurvivesTheRoundTrip(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	p := proposal("p1")
	if err := st.CreateProposal(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.Proposal(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Before != p.Before {
		t.Errorf("before: got %+v, want %+v", got.Before, p.Before)
	}
	if got.After != p.After {
		t.Errorf("after: got %+v, want %+v", got.After, p.After)
	}
}

// Approving a proposal written against a version somebody has since replaced
// would silently undo their work. The comparison that prevents it is a plain
// equality on the snapshot, so the snapshot has to survive storage exactly.
func TestSnapshotOfAStoredMemberEqualsTheProposalsBefore(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	stored, err := st.Member(ctx, "a1", stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProposal(ctx, Proposal{
		ID: "p1", MemberID: "a1", Kind: ProposeUpdate,
		Before: SnapshotOf(stored), After: SnapshotOf(stored),
		Status: Pending, ProposedBy: "ny@rudbeckia.nu", ProposedAt: onDay(2026, 6, 1),
	}); err != nil {
		t.Fatal(err)
	}
	p, err := st.Proposal(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	again, err := st.Member(ctx, "a1", stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if SnapshotOf(again) != p.Before {
		t.Errorf("an untouched member no longer matches its own snapshot:\n got %+v\nwant %+v",
			SnapshotOf(again), p.Before)
	}
}

func TestApplyWritesASnapshotBackOverAMember(t *testing.T) {
	m := anna()
	s := SnapshotOf(m)
	s.Phone, s.Kind, s.LeftOn = "070-9", config.KindVan, "2026-06-30"

	got, err := s.Apply(m, stockholm)
	if err != nil {
		t.Fatal(err)
	}
	if got.Phone != "070-9" || got.Kind != config.KindVan {
		t.Errorf("fields not applied: %+v", got)
	}
	if !got.LeftOn.Valid || got.LeftOn.Time.Day() != 30 {
		t.Errorf("leaving date not applied: %v", got.LeftOn)
	}
	// The bookkeeping columns are the database's, not the proposal's.
	if got.ID != m.ID || !got.CreatedAt.Equal(m.CreatedAt) {
		t.Error("Apply overwrote a column a proposal has no business in")
	}
}

func TestStaleProposalsAreRetiredWhenAMemberChanges(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateProposal(ctx, proposal("p1")); err != nil {
		t.Fatal(err)
	}
	if err := st.StaleProposalsFor(ctx, "a1", onDay(2026, 6, 5)); err != nil {
		t.Fatal(err)
	}
	got, err := st.Proposal(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != Stale {
		t.Errorf("status: got %q, want stale", got.Status)
	}
	n, err := st.CountPending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("pending count: got %d, want 0", n)
	}
}

// --- sync state -------------------------------------------------------------

// The board needs to tell an address that broke four minutes ago from one
// that has been broken for a fortnight, and only the second deserves a red
// banner. That distinction lives entirely in this column.
func TestFailingSinceRemembersWhenTheTroubleStarted(t *testing.T) {
	st, ctx := open(t), context.Background()
	const target, address = "group:bo@example.test", "anna@example.test"

	first := onDay(2026, time.June, 1)
	record := func(ok bool, at time.Time, msg string) {
		if err := st.RecordSyncState(ctx, SyncState{
			Target: target, Address: address, MemberID: "a1", Intent: Present,
			OK: ok, Message: msg, LastTry: at,
		}); err != nil {
			t.Fatal(err)
		}
	}

	record(true, first, "")
	record(false, first.Add(time.Hour), "Google said no")
	record(false, first.Add(2*time.Hour), "Google said no again")

	states, err := st.SyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("got %d rows, want 1 per address per target", len(states))
	}
	s := states[0]
	if s.OK {
		t.Error("the address should be recorded as failing")
	}
	// Still the hour it first broke, not the most recent attempt.
	if !s.FailingSince.Valid || !s.FailingSince.Time.Equal(first.Add(time.Hour).UTC()) {
		t.Errorf("failing since: got %v, want %s", s.FailingSince, first.Add(time.Hour).UTC())
	}
	if !s.LastOK.Valid || !s.LastOK.Time.Equal(first.UTC()) {
		t.Errorf("last ok: got %v, want %s", s.LastOK, first.UTC())
	}

	// And it is forgotten the moment the address works again.
	record(true, first.Add(3*time.Hour), "")
	states, err = st.SyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if states[0].FailingSince.Valid {
		t.Errorf("failing since should be cleared once it works: %v", states[0].FailingSince)
	}
}

func TestForgetSyncStateDropsAddressesNoLongerInPlay(t *testing.T) {
	st, ctx := open(t), context.Background()
	const target = "group:bo@example.test"
	for _, address := range []string{"a@x.test", "b@x.test", "c@x.test"} {
		if err := st.RecordSyncState(ctx, SyncState{Target: target, Address: address,
			Intent: Present, OK: true, LastTry: onDay(2026, 6, 1)}); err != nil {
			t.Fatal(err)
		}
	}
	// A whole-target row must survive, because it is not about an address.
	if err := st.RecordSyncState(ctx, SyncState{Target: target, OK: true,
		LastTry: onDay(2026, 6, 1)}); err != nil {
		t.Fatal(err)
	}

	if err := st.ForgetSyncState(ctx, target, []string{"a@x.test"}); err != nil {
		t.Fatal(err)
	}
	states, err := st.SyncStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]bool{}
	for _, s := range states {
		kept[s.Address] = true
	}
	if !kept["a@x.test"] || !kept[""] {
		t.Errorf("dropped something it should have kept: %v", kept)
	}
	if kept["b@x.test"] || kept["c@x.test"] {
		t.Errorf("kept something it should have dropped: %v", kept)
	}
}

func TestSyncTroubleReturnsOnlyTheFailures(t *testing.T) {
	st, ctx := open(t), context.Background()
	now := onDay(2026, 6, 1)
	if err := st.RecordSyncState(ctx, SyncState{Target: "t", Address: "ok@x.test",
		OK: true, LastTry: now}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSyncState(ctx, SyncState{Target: "t", Address: "bad@x.test",
		OK: false, Message: "nope", LastTry: now}); err != nil {
		t.Fatal(err)
	}
	trouble, err := st.SyncTrouble(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(trouble) != 1 || trouble[0].Address != "bad@x.test" {
		t.Errorf("got %+v, want only bad@x.test", trouble)
	}
}

func TestTheAuditTrailOutlivesTheMember(t *testing.T) {
	st, ctx := open(t), context.Background()
	if err := st.CreateMember(ctx, anna()); err != nil {
		t.Fatal(err)
	}
	if err := st.Log(ctx, Entry{At: onDay(2026, 6, 1), Actor: "styrelsen@rudbeckia.nu",
		Role: "board", Action: "member.removed", MemberID: "a1",
		Subject: "Anna Andersson <anna.andersson@example.test>",
		Detail:  "flyttade"}); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMember(ctx, "a1"); err != nil {
		t.Fatal(err)
	}

	entries, err := st.Audit(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1 — the trail is the record that survives", len(entries))
	}
	if entries[0].Subject == "" {
		t.Error("the trail must keep the name and address, or the line is unreadable afterwards")
	}
}

func TestSyncRunsAreTrimmed(t *testing.T) {
	st, ctx := open(t), context.Background()
	for i := 0; i < 20; i++ {
		if err := st.RecordSyncRun(ctx, SyncRun{
			StartedAt: onDay(2026, 6, 1), FinishedAt: onDay(2026, 6, 1),
			Trigger: "schedule", Target: "t", OK: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.TrimSyncRuns(ctx, 5); err != nil {
		t.Fatal(err)
	}
	runs, err := st.SyncRuns(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 5 {
		t.Errorf("got %d runs, want 5", len(runs))
	}
}
