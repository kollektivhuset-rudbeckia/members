// Package sync keeps Google Workspace in step with the register.
//
// The register is the source of truth. Every ten minutes — and within seconds
// of anybody changing anything — the reconciler works out what each Google
// group, address book and spreadsheet ought to contain, compares that with
// what they do contain, and makes up the difference.
//
// The important half of this package is not the making-up but the reporting.
// A silent synchroniser that has quietly failed for a fortnight is worse than
// no synchroniser at all, because the board will have spent the fortnight
// believing the groups were right. So every address gets a standing verdict
// in the database, a failure remembers when it started failing, and anything
// still broken after a grace period turns into a red banner on every page
// naming the exact addresses.
package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	stdsync "sync"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/google"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// Trigger says what set a reconciliation going. It is a closed set rather
// than free text because the sync page renders it as a translated phrase, and
// a trigger nobody thought to translate would show up as a missing-word
// marker in front of the board.
type Trigger string

const (
	// TriggerStart is the pass every start-up makes, so a restart does not
	// leave the groups wrong for up to ten minutes.
	TriggerStart Trigger = "start"
	// TriggerSchedule is the timer.
	TriggerSchedule Trigger = "schedule"
	// TriggerManual is somebody pressing the button on the sync page.
	TriggerManual Trigger = "manual"
	// TriggerMember is a member added, changed or removed.
	TriggerMember Trigger = "member"
	// TriggerPayment is a fee recorded or withdrawn. It reaches the
	// spreadsheet rather than the groups, but it is a change all the same.
	TriggerPayment Trigger = "payment"
	// TriggerProposal is the board approving somebody else's change.
	TriggerProposal Trigger = "proposal"
)

// Triggers are all of them, for the test that checks the catalogue has a
// phrase for each.
var Triggers = []Trigger{TriggerStart, TriggerSchedule, TriggerManual,
	TriggerMember, TriggerPayment, TriggerProposal}

// Syncer reconciles the register with Google.
type Syncer struct {
	cfg   *config.Config
	rt    config.Runtime
	store *store.Store
	gc    *google.Client
	log   *slog.Logger
	now   func() time.Time

	// nudge carries a reason from whoever just changed something. It is
	// buffered and non-blocking: a handler must never wait on the syncer.
	nudge chan Trigger

	// running serialises runs. Two reconciliations at once would race each
	// other into adding the same address twice and reporting the loser as a
	// failure, so a second one waits rather than starting.
	running stdsync.Mutex

	mu       stdsync.Mutex
	last     Report
	progress Progress
}

// Progress is what a run is doing right now, for a page that has to say
// something more useful than nothing while it works.
//
// A first run against three address books is several hundred calls to Google
// and takes minutes. Without this the button either appears to do nothing or
// times out, and the person presses it again.
type Progress struct {
	Running bool
	// Only is the single target being reconciled, empty for all of them.
	Only    string
	Trigger Trigger
	Since   time.Time
	// Done and Total count targets, not addresses: it is the honest unit,
	// because the reconciler does not know how much work a target holds
	// until it has asked Google.
	Done  int
	Total int
	// Now is the target being worked on.
	Now string
}

// Progress reports what is happening, if anything.
func (s *Syncer) Progress() Progress {
	if s == nil {
		return Progress{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.progress
}

func (s *Syncer) setProgress(f func(*Progress)) {
	s.mu.Lock()
	f(&s.progress)
	s.mu.Unlock()
}

// Report is what one whole pass did, per target.
type Report struct {
	At      time.Time
	Trigger Trigger
	Runs    []store.SyncRun
	// Err is set when the pass could not even be started — the register could
	// not be read, say. A target that failed on its own is in Runs.
	Err error
}

// OK reports whether every target in the pass succeeded.
func (r Report) OK() bool {
	if r.Err != nil {
		return false
	}
	for _, run := range r.Runs {
		if !run.OK {
			return false
		}
	}
	return true
}

// New builds a Syncer. gc may be nil, in which case nothing is synchronised
// and the pages say so; that is the state a fresh deployment starts in and it
// has to be a comprehensible one rather than a crash.
func New(cfg *config.Config, rt config.Runtime, st *store.Store, gc *google.Client, log *slog.Logger) *Syncer {
	return &Syncer{
		cfg: cfg, rt: rt, store: st, gc: gc, log: log,
		now:   time.Now,
		nudge: make(chan Trigger, 1),
	}
}

// Enabled reports whether there is anything to synchronise with.
func (s *Syncer) Enabled() bool { return s != nil && s.gc != nil }

// Last returns the most recent pass, for the sync page.
func (s *Syncer) Last() Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last
}

// Nudge asks for a reconciliation soon. It never blocks: if one is already
// queued, this change rides along with it, which is exactly what should
// happen when five members are added in a row.
func (s *Syncer) Nudge(reason Trigger) {
	if !s.Enabled() {
		return
	}
	select {
	case s.nudge <- reason:
	default:
	}
}

// Run is the background loop. It returns when ctx is cancelled.
func (s *Syncer) Run(ctx context.Context) {
	if !s.Enabled() {
		s.log.Warn("nothing is being synchronised: no Google service account is configured")
		return
	}
	interval := s.cfg.Sync.Interval()
	s.log.Info("synchronisation is on", "every", interval, "targets", s.cfg.Targets())

	// A pass on start-up, so a restart does not leave the groups wrong for up
	// to ten minutes and so a misconfiguration shows up straight away.
	s.Once(ctx, TriggerStart)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Once(ctx, TriggerSchedule)
		case reason := <-s.nudge:
			// Let the neighbours arrive before pushing. Adding a household of
			// four should be one synchronisation, not four.
			settle := time.NewTimer(s.cfg.Sync.Settle())
			draining := true
			for draining {
				select {
				case <-ctx.Done():
					settle.Stop()
					return
				case <-s.nudge:
					// Another change: start the quiet period again.
					if !settle.Stop() {
						<-settle.C
					}
					settle.Reset(s.cfg.Sync.Settle())
				case <-settle.C:
					draining = false
				}
			}
			s.Once(ctx, reason)
		}
	}
}

// Once runs one full pass over every target, waiting if another is running.
func (s *Syncer) Once(ctx context.Context, trigger Trigger) Report {
	return s.once(ctx, trigger, "", true)
}

// ErrBusy is returned when a run was asked for and one is already going.
var ErrBusy = errors.New("a synchronisation is already running")

// Start begins a run in the background and returns at once.
//
// only names a single target, or is empty for all of them. It refuses rather
// than queues when one is already going: somebody pressing a button wants to
// know whether it did anything, and "it will happen eventually" is not an
// answer a page can act on.
func (s *Syncer) Start(trigger Trigger, only string) error {
	if !s.Enabled() {
		return errors.New("no Google service account is configured")
	}
	if !s.running.TryLock() {
		return ErrBusy
	}
	// The request that started this is long gone by the time it finishes, so
	// the run gets a context of its own. The bound is generous: a first pass
	// over three address books is several hundred calls to Google.
	// Mark it running here rather than in the goroutine. The caller redirects
	// to a page that asks "is anything happening?" immediately afterwards,
	// and losing that race would answer no.
	s.setProgress(func(p *Progress) {
		*p = Progress{Running: true, Only: only, Trigger: trigger, Since: s.now()}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	go func() {
		defer cancel()
		defer s.running.Unlock()
		s.once(ctx, trigger, only, false)
	}()
	return nil
}

// once does the work. lock says whether to take the run lock, which Start has
// already done for itself.
func (s *Syncer) once(ctx context.Context, trigger Trigger, only string, lock bool) Report {
	if !s.Enabled() {
		return Report{At: s.now(), Trigger: trigger,
			Err: errors.New("no Google service account is configured")}
	}
	if lock {
		s.running.Lock()
		defer s.running.Unlock()
	}

	report := Report{At: s.now(), Trigger: trigger}
	loc := s.cfg.Location()

	wanted := func(target string) bool { return only == "" || only == target }
	total := 0
	for _, t := range s.cfg.Targets() {
		if wanted(t) {
			total++
		}
	}
	s.setProgress(func(p *Progress) {
		since := s.now()
		if p.Running && !p.Since.IsZero() {
			since = p.Since // Start already began the clock
		}
		*p = Progress{Running: true, Only: only, Trigger: trigger,
			Since: since, Total: total}
	})
	defer s.setProgress(func(p *Progress) { p.Running, p.Now = false, "" })

	members, err := s.store.Members(ctx, loc)
	if err != nil {
		report.Err = fmt.Errorf("read the register: %w", err)
		s.remember(report)
		s.log.Error("synchronisation could not start", "err", report.Err)
		return report
	}

	// Only current members belong in a group or an address book. Somebody who
	// has left keeps their row and their history and loses their mail.
	// A member belongs in the group for their own kind, and in any group they
	// have been added to as well — a bomedlem who runs a matlag has to be
	// able to write to the vänmedlemmar, and Google only takes post from
	// somebody who is in the group.
	want := map[config.Kind][]store.Member{}
	byKey := map[string]store.Member{}
	for _, m := range members {
		byKey[s.cfg.Sync.MatchKey(m.Email)] = m
		if !m.Current() {
			continue
		}
		for _, kind := range config.Kinds {
			if m.In(kind) {
				want[kind] = append(want[kind], m)
			}
		}
	}

	step := func(target string, run func() store.SyncRun) {
		if !wanted(target) {
			return
		}
		s.setProgress(func(p *Progress) { p.Now = target })
		report.Runs = append(report.Runs, run())
		s.setProgress(func(p *Progress) { p.Done++ })
	}

	for _, g := range s.cfg.Groups {
		g := g
		step("group:"+g.Email, func() store.SyncRun {
			return s.syncGroup(ctx, trigger, g, want[g.Kind])
		})
	}
	for _, mailbox := range s.cfg.Contacts.Accounts {
		mailbox := mailbox
		step("contacts:"+mailbox, func() store.SyncRun {
			return s.syncContacts(ctx, trigger, mailbox, want, byKey)
		})
	}
	if s.cfg.Sheet.Enabled() {
		step("sheet", func() store.SyncRun { return s.syncSheet(ctx, trigger, members) })
	}

	for _, run := range report.Runs {
		if err := s.store.RecordSyncRun(ctx, run); err != nil {
			s.log.Error("could not record a sync run", "target", run.Target, "err", err)
		}
	}
	if err := s.store.TrimSyncRuns(ctx, 2000); err != nil {
		s.log.Warn("could not trim the sync log", "err", err)
	}

	s.remember(report)
	if report.OK() {
		s.log.Info("synchronised", "trigger", trigger, "targets", len(report.Runs))
	} else {
		s.log.Error("synchronisation had problems", "trigger", trigger,
			"targets", len(report.Runs), "trouble", troubleSummary(report))
	}
	return report
}

func (s *Syncer) remember(r Report) {
	s.mu.Lock()
	s.last = r
	s.mu.Unlock()
}

func troubleSummary(r Report) string {
	var parts []string
	for _, run := range r.Runs {
		if !run.OK {
			parts = append(parts, run.Target+": "+run.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// admin is the Workspace administrator group operations run as.
func (s *Syncer) admin() string { return s.rt.Google.AdminSubject }

// protected reports whether an address must never be removed from a group:
// the register's own sign-in accounts, and whatever the group lists as kept.
func (s *Syncer) protected(g config.Group, email string) bool {
	if g.Kept(email) {
		return true
	}
	_, isAccount := s.rt.Accounts[store.Email(email)]
	return isAccount
}

// syncGroup makes one Google group hold exactly the register's addresses.
func (s *Syncer) syncGroup(ctx context.Context, trigger Trigger, g config.Group,
	want []store.Member) store.SyncRun {

	target := "group:" + g.Email
	run := store.SyncRun{StartedAt: s.now(), Trigger: string(trigger), Target: target, OK: true}
	finish := func() store.SyncRun {
		run.FinishedAt = s.now()
		return run
	}

	// An empty register is not an instruction to empty the group.
	//
	// This is the state every deployment starts in, and the first thing it
	// does is reconcile. Without this guard the very first run of a correctly
	// configured registry removes every member of the house from their
	// mailing list, and the only record of who they were is the log.
	if len(want) == 0 {
		run.OK = false
		run.Message = "the register holds no current " + string(g.Kind) +
			" members, so the group was left alone — fill the register first (see -import)"
		s.recordTarget(ctx, target, false, run.Message)
		s.log.Warn("refusing to touch a group from an empty register",
			"group", g.Email, "kind", g.Kind)
		return finish()
	}

	have, err := s.gc.GroupMembers(ctx, s.admin(), g.Email)
	if err != nil {
		run.OK, run.Failed = false, 1
		run.Message = err.Error()
		s.recordTarget(ctx, target, false, err.Error())
		return finish()
	}
	s.recordTarget(ctx, target, true, "")

	// Google's own view, keyed on the comparison form rather than the literal
	// address: a Gmail mailbox spelled with dots in the group and without them
	// in the register is one mailbox, and treating it as two would have the
	// registry adding it forever and reporting the member as broken.
	present := make(map[string]google.GroupMember, len(have))
	for _, m := range have {
		if key := s.cfg.Sync.MatchKey(m.Email); key != "" {
			present[key] = m
		}
	}

	wanted := make(map[string]store.Member, len(want))
	var touched []string
	for _, m := range want {
		email := store.Email(m.Email)
		key := s.cfg.Sync.MatchKey(m.Email)
		wanted[key] = m
		touched = append(touched, email)

		if _, ok := present[key]; ok {
			s.recordAddress(ctx, target, email, m.ID, store.Present, true, "")
			continue
		}
		if err := s.gc.AddToGroup(ctx, s.admin(), g.Email, m.Email, g.MemberRole()); err != nil {
			run.Failed++
			run.OK = false
			s.recordAddress(ctx, target, email, m.ID, store.Present, false, err.Error())
			continue
		}
		run.Added++
		s.recordAddress(ctx, target, email, m.ID, store.Present, true, "")
	}

	// Everything in the group the register does not know about, worked out in
	// full before a single removal is made. Counting first is what lets the
	// brake below see the size of what is about to happen.
	var strays []string
	for key, gm := range present {
		if _, ok := wanted[key]; ok {
			continue
		}
		// Owners and managers are the group's own scaffolding. The register
		// administers members; it does not administer the group.
		if role := strings.ToUpper(gm.Role); role != "" && role != "MEMBER" {
			continue
		}
		// The address as Google spells it, which is what a removal must use
		// and what the board should see on the page.
		email := store.Email(gm.Email)
		if s.protected(g, email) {
			continue
		}
		strays = append(strays, email)
	}
	sort.Strings(strays)

	// A pass that wants to remove more than a handful is a mistake, not a
	// busy week. Report it and change nothing: putting a group back by hand
	// is an evening's work, and reading a list is a minute.
	brake := s.cfg.Sync.MaxRemovalsPerRun
	held := brake > 0 && len(strays) > brake

	for _, email := range strays {
		touched = append(touched, email)
		if held {
			run.OK = false
			run.Failed++
			s.recordAddress(ctx, target, email, "", store.Absent, false,
				fmt.Sprintf("would be removed, but %d addresses at once is more than "+
					"max_removals_per_run (%d) — check the list, then raise the limit "+
					"or add them to the register", len(strays), brake))
			continue
		}
		if !g.Pruning() {
			run.OK = false
			run.Failed++
			s.recordAddress(ctx, target, email, "", store.Absent, false,
				"is in the group but not in the register, and pruning is off")
			continue
		}
		if err := s.gc.RemoveFromGroup(ctx, s.admin(), g.Email, email); err != nil {
			run.Failed++
			run.OK = false
			s.recordAddress(ctx, target, email, "", store.Absent, false, err.Error())
			continue
		}
		run.Removed++
		s.log.Info("removed a stray address from a group", "group", g.Email, "address", email)
	}
	if held {
		run.Message = fmt.Sprintf("%d addresses would have been removed, which is over "+
			"max_removals_per_run (%d); nothing was changed", len(strays), brake)
		s.log.Warn("refusing a bulk removal", "group", g.Email,
			"would_remove", len(strays), "limit", brake)
	}

	// Forget the addresses this target no longer has an opinion about, so an
	// old failure for somebody who has since left does not haunt the page.
	if err := s.store.ForgetSyncState(ctx, target, touched); err != nil {
		s.log.Warn("could not tidy the sync state", "target", target, "err", err)
	}
	if !run.OK && run.Message == "" {
		run.Message = fmt.Sprintf("%d addresses could not be synchronised", run.Failed)
	}
	return finish()
}

// syncSheet writes the whole register into the cashiers' spreadsheet.
func (s *Syncer) syncSheet(ctx context.Context, trigger Trigger, members []store.Member) store.SyncRun {
	run := store.SyncRun{StartedAt: s.now(), Trigger: string(trigger), Target: "sheet", OK: true}
	finish := func() store.SyncRun {
		run.FinishedAt = s.now()
		return run
	}

	payments, err := s.store.AllPayments(ctx, s.cfg.Location())
	if err != nil {
		run.OK, run.Message = false, fmt.Sprintf("read the payments: %v", err)
		s.recordTarget(ctx, "sheet", false, run.Message)
		return finish()
	}

	mailbox := s.sheetMailbox()
	info, err := s.gc.Sheet(ctx, mailbox, s.cfg.Sheet.ID, s.cfg.Sheet.TabName())
	if err != nil {
		run.OK, run.Message = false, err.Error()
		s.recordTarget(ctx, "sheet", false, err.Error())
		return finish()
	}

	roster := membership.Build(members, payments, s.cfg, s.now())
	grid := Grid(roster, s.cfg, s.now())
	if err := s.gc.WriteSheet(ctx, mailbox, s.cfg.Sheet.ID, info.Title, grid); err != nil {
		run.OK, run.Message = false, err.Error()
		s.recordTarget(ctx, "sheet", false, err.Error())
		return finish()
	}
	// Cosmetic, and never worth failing the run over.
	if err := s.gc.FreezeHeader(ctx, mailbox, s.cfg.Sheet.ID, info.SheetID); err != nil {
		s.log.Debug("could not freeze the spreadsheet header", "err", err)
	}

	run.Updated = len(members)
	s.recordTarget(ctx, "sheet", true, "")
	return finish()
}

// sheetMailbox is the account the spreadsheet is written as. The board's own
// mailbox is the sensible owner of a document the board reads, and it saves
// having to share the sheet with the service account by hand.
func (s *Syncer) sheetMailbox() string {
	if board := s.rt.AccountFor(config.RoleBoard); board != "" {
		return board
	}
	return s.admin()
}

// recordTarget writes the verdict on a target as a whole.
func (s *Syncer) recordTarget(ctx context.Context, target string, ok bool, message string) {
	err := s.store.RecordSyncState(ctx, store.SyncState{
		Target: target, Address: "", OK: ok, Message: message, LastTry: s.now(),
	})
	if err != nil {
		s.log.Error("could not record the sync state", "target", target, "err", err)
	}
}

// recordAddress writes the verdict on one address at one target.
func (s *Syncer) recordAddress(ctx context.Context, target, email, memberID string,
	intent store.Intent, ok bool, message string) {

	err := s.store.RecordSyncState(ctx, store.SyncState{
		Target: target, Address: email, MemberID: memberID, Intent: intent,
		OK: ok, Message: message, LastTry: s.now(),
	})
	if err != nil {
		s.log.Error("could not record the sync state", "target", target, "address", email, "err", err)
	}
	if !ok {
		s.log.Warn("an address would not synchronise",
			"target", target, "address", email, "reason", message)
	}
}

// Trouble is one thing that is currently wrong, ready for a page.
type Trouble struct {
	State store.SyncState
	// Loud reports whether this has been failing long enough to deserve the
	// banner. Something that broke ninety seconds ago and will very likely fix
	// itself on the next pass should not cry wolf.
	Loud bool
}

// Trouble returns everything currently out of step, worst first.
func (s *Syncer) Trouble(ctx context.Context, st *store.Store) ([]Trouble, error) {
	states, err := st.SyncTrouble(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := s.now().Add(-s.cfg.Sync.AlertAfter())
	out := make([]Trouble, 0, len(states))
	for _, state := range states {
		// A whole target that is unreachable is always loud: it is never a
		// transient one-address hiccup, and it means nothing is being
		// synchronised at all.
		loud := state.WholeTarget() ||
			(state.FailingSince.Valid && state.FailingSince.Time.Before(cutoff))
		out = append(out, Trouble{State: state, Loud: loud})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Loud != out[j].Loud {
			return out[i].Loud
		}
		return out[i].State.Target < out[j].State.Target
	})
	return out, nil
}

// Verify checks at start-up that every target is actually reachable, so a
// typo in config.yaml or a missing scope is a line in the log within seconds
// rather than a mystery ten minutes later. It never stops the server: a
// registry that will not start because Google is having a bad morning is
// worse than one that starts and says so.
func (s *Syncer) Verify(ctx context.Context) []error {
	if !s.Enabled() {
		return nil
	}
	var problems []error
	for _, g := range s.cfg.Groups {
		if err := s.gc.GroupExists(ctx, s.admin(), g.Email); err != nil {
			problems = append(problems, err)
		}
	}
	for _, mailbox := range s.cfg.Contacts.Accounts {
		for _, kind := range config.Kinds {
			if _, err := s.gc.EnsureLabel(ctx, mailbox, s.cfg.Contacts.LabelFor(kind)); err != nil {
				problems = append(problems, err)
				break
			}
		}
	}
	if s.cfg.Sheet.Enabled() {
		if _, err := s.gc.Sheet(ctx, s.sheetMailbox(), s.cfg.Sheet.ID, s.cfg.Sheet.TabName()); err != nil {
			problems = append(problems, err)
		}
	}
	return problems
}
