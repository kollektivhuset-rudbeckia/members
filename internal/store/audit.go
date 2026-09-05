package store

import (
	"context"
	"time"
)

// Entry is one line of the audit trail: who did what to whom, and when.
//
// The trail is append-only and is the reason a member row can be deleted
// outright. The association needs to be able to answer "who took Anna off the
// list, and when?" long after Anna's row is gone.
type Entry struct {
	ID       int64
	At       time.Time
	Actor    string
	Role     string
	Action   string
	MemberID string
	// Subject is the member's name and address as they were at the time, so
	// the line still reads properly after the row itself has gone.
	Subject string
	Detail  string
}

// Log appends to the audit trail. A failure to write the trail must never
// fail the operation it was recording, so callers log the error and move on —
// but the trail is written in the same request, not in a goroutine, so that
// it cannot be lost to a shutdown.
func (s *Store) Log(ctx context.Context, e Entry) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO audit (at, actor, role, action, member_id, subject, detail)
		VALUES (?,?,?,?,?,?,?)`,
		utc(e.At), e.Actor, e.Role, e.Action, e.MemberID, e.Subject, e.Detail)
	return err
}

// Audit returns the most recent trail entries.
func (s *Store) Audit(ctx context.Context, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	return s.queryAudit(ctx, `SELECT id, at, actor, role, action, member_id, subject, detail
		FROM audit ORDER BY at DESC, id DESC LIMIT ?`, limit)
}

// AuditFor returns the trail for one member, newest first.
func (s *Store) AuditFor(ctx context.Context, memberID string, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queryAudit(ctx, `SELECT id, at, actor, role, action, member_id, subject, detail
		FROM audit WHERE member_id = ? ORDER BY at DESC, id DESC LIMIT ?`, memberID, limit)
}

func (s *Store) queryAudit(ctx context.Context, q string, args ...any) ([]Entry, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var at string
		if err := rows.Scan(&e.ID, &at, &e.Actor, &e.Role, &e.Action, &e.MemberID,
			&e.Subject, &e.Detail); err != nil {
			return nil, err
		}
		if e.At, err = time.Parse(time.RFC3339, at); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
