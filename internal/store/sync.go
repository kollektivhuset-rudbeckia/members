package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// Intent is what the registry wanted an address to be, at a target.
type Intent string

const (
	// Present means the address belongs there.
	Present Intent = "present"
	// Absent means it does not, and was removed or should be.
	Absent Intent = "absent"
)

// SyncState is the standing verdict on one address at one target: whether the
// last attempt to make Google agree with the register worked, and if not, for
// how long it has been failing.
//
// One row per address rather than one per run, because what the board needs
// on the page is "which addresses are wrong right now" — not a transcript.
// The transcript is SyncRun.
type SyncState struct {
	// Target is "group:bomedlemmar@rudbeckia.nu", "contacts:ny@rudbeckia.nu"
	// or "sheet". A whole-target problem — the group does not exist, the
	// credentials are refused — is recorded with an empty Address.
	Target  string
	Address string
	// MemberID is empty for a stray address that is in Google but not in the
	// register, which is exactly the case worth showing the board.
	MemberID     string
	Intent       Intent
	OK           bool
	Message      string
	FailingSince sql.NullTime
	LastOK       sql.NullTime
	LastTry      time.Time
}

// Kind splits the target into its sort and its subject, so a page can group
// by "group" or "contacts" without parsing strings in a template.
func (s SyncState) Kind() string {
	if i := strings.IndexByte(s.Target, ':'); i > 0 {
		return s.Target[:i]
	}
	return s.Target
}

// Subject is the group address, mailbox or spreadsheet the target names.
func (s SyncState) Subject() string {
	if i := strings.IndexByte(s.Target, ':'); i > 0 {
		return s.Target[i+1:]
	}
	return ""
}

// WholeTarget reports whether this is a problem with the target itself rather
// than with one address in it.
func (s SyncState) WholeTarget() bool { return s.Address == "" }

// RecordSyncState writes the verdict for one address at one target.
//
// failing_since is only set when a working address starts failing, and only
// cleared when it works again. That is the whole point of the column: the
// board can tell an address that has been broken for a fortnight from one
// that broke four minutes ago, and only the first deserves shouting about.
func (s *Store) RecordSyncState(ctx context.Context, st SyncState) error {
	ok := 0
	if st.OK {
		ok = 1
	}
	var lastOK any
	if st.OK {
		lastOK = utc(st.LastTry)
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_state (target, address, member_id, intent, ok, message,
			failing_since, last_ok, last_try)
		VALUES (?,?,?,?,?,?, CASE WHEN ? = 1 THEN NULL ELSE ? END, ?, ?)
		ON CONFLICT(target, address) DO UPDATE SET
			member_id = excluded.member_id,
			intent = excluded.intent,
			ok = excluded.ok,
			message = excluded.message,
			failing_since = CASE
				WHEN excluded.ok = 1 THEN NULL
				WHEN sync_state.failing_since IS NULL THEN excluded.last_try
				ELSE sync_state.failing_since END,
			last_ok = COALESCE(excluded.last_ok, sync_state.last_ok),
			last_try = excluded.last_try`,
		st.Target, Email(st.Address), st.MemberID, string(st.Intent), ok, st.Message,
		ok, utc(st.LastTry), lastOK, utc(st.LastTry))
	return err
}

// ForgetSyncState drops rows for addresses a target no longer has an opinion
// about — somebody who was removed from the register and successfully removed
// from the group has nothing left to report.
func (s *Store) ForgetSyncState(ctx context.Context, target string, keep []string) error {
	if len(keep) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM sync_state WHERE target = ? AND address <> ''`, target)
		return err
	}
	// Built rather than parameterised in one go: SQLite's parameter limit is
	// generous but not infinite, and a register of a few hundred addresses
	// makes a placeholder list that is comfortably inside it.
	marks := make([]string, len(keep))
	args := make([]any, 0, len(keep)+1)
	args = append(args, target)
	for i, address := range keep {
		marks[i] = "?"
		args = append(args, Email(address))
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sync_state WHERE target = ? AND address <> '' AND address NOT IN (`+
			strings.Join(marks, ",")+`)`, args...)
	return err
}

// SyncStates returns every standing verdict, worst first: the failures, then
// the addresses that are fine.
func (s *Store) SyncStates(ctx context.Context) ([]SyncState, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT target, address, member_id, intent, ok, message, failing_since, last_ok, last_try
		FROM sync_state ORDER BY ok ASC, failing_since ASC, target ASC, address ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncState
	for rows.Next() {
		var st SyncState
		var intent string
		var ok int
		var since, lastOK sql.NullString
		var lastTry string
		if err := rows.Scan(&st.Target, &st.Address, &st.MemberID, &intent, &ok,
			&st.Message, &since, &lastOK, &lastTry); err != nil {
			return nil, err
		}
		st.Intent, st.OK = Intent(intent), ok == 1
		if st.FailingSince, err = nullTime(since); err != nil {
			return nil, err
		}
		if st.LastOK, err = nullTime(lastOK); err != nil {
			return nil, err
		}
		if st.LastTry, err = time.Parse(time.RFC3339, lastTry); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// SyncTrouble returns only the failures, which is what the banner counts.
func (s *Store) SyncTrouble(ctx context.Context) ([]SyncState, error) {
	all, err := s.SyncStates(ctx)
	if err != nil {
		return nil, err
	}
	var out []SyncState
	for _, st := range all {
		if !st.OK {
			out = append(out, st)
		}
	}
	return out, nil
}

// SyncRun is one pass over one target: the transcript the board reads when
// they want to know what the registry has been doing.
type SyncRun struct {
	ID         int64
	StartedAt  time.Time
	FinishedAt time.Time
	Trigger    string
	Target     string
	OK         bool
	Added      int
	Removed    int
	Updated    int
	Failed     int
	Message    string
}

// Changed reports whether the run actually did anything, so the page can dim
// the great majority of runs that found nothing to do.
func (r SyncRun) Changed() bool { return r.Added+r.Removed+r.Updated > 0 }

// Duration is how long the pass took.
func (r SyncRun) Duration() time.Duration { return r.FinishedAt.Sub(r.StartedAt) }

// RecordSyncRun appends to the transcript.
func (s *Store) RecordSyncRun(ctx context.Context, r SyncRun) error {
	ok := 0
	if r.OK {
		ok = 1
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sync_runs (started_at, finished_at, trigger, target, ok,
			added, removed, updated, failed, message)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		utc(r.StartedAt), utc(r.FinishedAt), r.Trigger, r.Target, ok,
		r.Added, r.Removed, r.Updated, r.Failed, r.Message)
	return err
}

// SyncRuns returns the most recent passes, newest first.
func (s *Store) SyncRuns(ctx context.Context, limit int) ([]SyncRun, error) {
	if limit <= 0 {
		limit = 60
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, started_at, finished_at, trigger, target, ok, added, removed, updated, failed, message
		FROM sync_runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SyncRun
	for rows.Next() {
		var r SyncRun
		var started, finished string
		var ok int
		if err := rows.Scan(&r.ID, &started, &finished, &r.Trigger, &r.Target, &ok,
			&r.Added, &r.Removed, &r.Updated, &r.Failed, &r.Message); err != nil {
			return nil, err
		}
		r.OK = ok == 1
		if r.StartedAt, err = time.Parse(time.RFC3339, started); err != nil {
			return nil, err
		}
		if r.FinishedAt, err = time.Parse(time.RFC3339, finished); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TrimSyncRuns keeps the transcript from growing without bound. Ten minutes
// between runs and a handful of targets is a few hundred rows a day, and
// nobody has ever needed last spring's.
func (s *Store) TrimSyncRuns(ctx context.Context, keep int) error {
	if keep <= 0 {
		keep = 2000
	}
	_, err := s.db.ExecContext(ctx, `
		DELETE FROM sync_runs WHERE id NOT IN (
			SELECT id FROM sync_runs ORDER BY id DESC LIMIT ?)`, keep)
	return err
}

func nullTime(s sql.NullString) (sql.NullTime, error) {
	if !s.Valid || strings.TrimSpace(s.String) == "" {
		return sql.NullTime{}, nil
	}
	t, err := time.Parse(time.RFC3339, s.String)
	if err != nil {
		return sql.NullTime{}, err
	}
	return sql.NullTime{Time: t, Valid: true}, nil
}
