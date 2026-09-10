package store

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// Candidate is somebody on their way towards the house.
//
// It is one thing rather than two on purpose. Somebody who fills in the form
// on the public page and somebody the interview team wrote down after meeting
// them at an open house are at the same point in the same process, and the
// team should not have to look in two places to find out who is waiting. The
// form is the front door of the pipeline, not a separate inbox.
type Candidate struct {
	ID string
	// Token addresses the page that thanks somebody and tells them how to
	// pay. It is unguessable so that the pipeline cannot be read by counting.
	Token     string
	FirstName string
	LastName  string
	Email     string
	Phone     string
	// Apartment is the flat they are interested in, which is what the old
	// board recorded — the interview team works flat by flat.
	Apartment string
	Kind      config.Kind
	// Message is what they wrote about themselves.
	Message string
	// Reason is why they want to join: one of the ids in config.Join, or
	// config.ReasonOther. Empty on candidates recorded before the form asked,
	// which reads as "nobody asked" rather than as a reason nobody chose.
	Reason string
	// ReasonNote is what they typed when no set answer fitted. It is only
	// filled in alongside config.ReasonOther.
	ReasonNote string
	// Stage is where they have reached, one of the ids in config.Pipeline.
	Stage string
	// Responsible is whoever on the team is looking after them, by name.
	Responsible string
	InterviewOn sql.NullTime
	Note        string
	// Source says where the row came from: the public form, the import, or
	// somebody typing it in.
	Source string
	// MemberID is filled in once they became a member.
	MemberID  string
	CreatedAt time.Time
	CreatedIP string
	MovedAt   time.Time
	MovedBy   string
}

// Name is how the candidate is written on the board.
func (c Candidate) Name() string { return joinName(c.FirstName, c.LastName) }

// Sources a candidate can come from.
const (
	SourceForm   = "form"
	SourceImport = "import"
	SourceByHand = "hand"
)

const candidateCols = `id, token, first_name, last_name, email, phone, apartment, kind,
	message, reason, reason_note, stage, responsible, interview_on, note, source, member_id,
	created_at, created_ip, moved_at, moved_by`

// CreateCandidate writes a new one.
func (s *Store) CreateCandidate(ctx context.Context, c Candidate) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO candidates (`+candidateCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.ID, c.Token, c.FirstName, c.LastName, Email(c.Email), c.Phone, c.Apartment,
		string(c.Kind), c.Message, c.Reason, c.ReasonNote,
		c.Stage, c.Responsible, dayOrNil(c.InterviewOn),
		c.Note, c.Source, c.MemberID,
		utc(c.CreatedAt), c.CreatedIP, utc(c.MovedAt), c.MovedBy)
	return err
}

// UpdateCandidate rewrites everything the team may change.
func (s *Store) UpdateCandidate(ctx context.Context, c Candidate) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE candidates SET first_name=?, last_name=?, email=?, phone=?, apartment=?,
			kind=?, message=?, reason=?, reason_note=?, stage=?, responsible=?,
			interview_on=?, note=?, member_id=?, moved_at=?, moved_by=?
		WHERE id=?`,
		c.FirstName, c.LastName, Email(c.Email), c.Phone, c.Apartment,
		string(c.Kind), c.Message, c.Reason, c.ReasonNote,
		c.Stage, c.Responsible, dayOrNil(c.InterviewOn),
		c.Note, c.MemberID, utc(c.MovedAt), c.MovedBy, c.ID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCandidate takes one card off the board for good.
//
// Unlike removing a member this needs nobody's approval, because it is not
// the same kind of act. A card is the interview team's own note about
// somebody the association has not yet agreed anything with, and the
// register — the thing the association is actually accountable for — is a
// different table that this statement cannot reach. Somebody who was
// welcomed stays a member after their card is gone.
func (s *Store) DeleteCandidate(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM candidates WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ClearCandidateStage empties one stage and returns the cards it removed.
//
// It reads the rows before deleting them, and returns them, rather than
// reporting a count afterwards. "12 cards were removed" is not something
// anybody can check a month later; the names are the only part of a deleted
// card still worth having, and the caller writes them into the audit trail.
//
// Whether a stage may be cleared at all is not decided here — that is the
// caller's business, and it turns on the stage being closed.
func (s *Store) ClearCandidateStage(ctx context.Context, stage string,
	loc *time.Location) ([]Candidate, error) {

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx,
		`SELECT `+candidateCols+` FROM candidates WHERE stage = ? ORDER BY created_at`, stage)
	if err != nil {
		return nil, err
	}
	var gone []Candidate
	for rows.Next() {
		c, err := scanCandidate(rows, loc)
		if err != nil {
			rows.Close()
			return nil, err
		}
		gone = append(gone, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if len(gone) == 0 {
		return nil, tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM candidates WHERE stage = ?`, stage); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return gone, nil
}

// Candidate returns one by id.
func (s *Store) Candidate(ctx context.Context, id string, loc *time.Location) (Candidate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+candidateCols+` FROM candidates WHERE id = ?`, id)
	c, err := scanCandidate(row, loc)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// CandidateByToken returns one by the token in its thank-you address.
func (s *Store) CandidateByToken(ctx context.Context, token string, loc *time.Location) (Candidate, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+candidateCols+` FROM candidates WHERE token = ?`, token)
	c, err := scanCandidate(row, loc)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// Candidates returns the whole board, newest first within a stage.
func (s *Store) Candidates(ctx context.Context, loc *time.Location) ([]Candidate, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+candidateCols+` FROM candidates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Candidate
	for rows.Next() {
		c, err := scanCandidate(rows, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// CountCandidatesIn counts the ones sitting in given stages, for the badge in
// the top bar: somebody who has sent a form and heard nothing is the worst
// thing this register can do to a person.
func (s *Store) CountCandidatesIn(ctx context.Context, stages []string) (int, error) {
	if len(stages) == 0 {
		return 0, nil
	}
	marks := make([]string, len(stages))
	args := make([]any, len(stages))
	for i, st := range stages {
		marks[i], args[i] = "?", st
	}
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM candidates WHERE stage IN (`+strings.Join(marks, ",")+`)`,
		args...).Scan(&n)
	return n, err
}

// OpenCandidateByEmail finds somebody already in the pipeline at that address,
// so that pressing the button twice does not put them on the board twice.
func (s *Store) OpenCandidateByEmail(ctx context.Context, email string, closed []string,
	loc *time.Location) (Candidate, error) {

	q := `SELECT ` + candidateCols + ` FROM candidates WHERE email = ?`
	args := []any{Email(email)}
	if len(closed) > 0 {
		marks := make([]string, len(closed))
		for i, st := range closed {
			marks[i] = "?"
			args = append(args, st)
		}
		q += ` AND stage NOT IN (` + strings.Join(marks, ",") + `)`
	}
	q += ` ORDER BY created_at DESC LIMIT 1`

	row := s.db.QueryRowContext(ctx, q, args...)
	c, err := scanCandidate(row, loc)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

// RecentCandidatesFrom counts what one address has sent lately, which is how
// a public form avoids being a way to fill the database up.
func (s *Store) RecentCandidatesFrom(ctx context.Context, ip string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM candidates WHERE created_ip = ? AND created_at > ?`,
		ip, utc(since)).Scan(&n)
	return n, err
}

// CountCandidates is how many the board holds, for the importer.
func (s *Store) CountCandidates(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM candidates`).Scan(&n)
	return n, err
}

func dayOrNil(t sql.NullTime) any {
	if !t.Valid {
		return nil
	}
	return day(t.Time)
}

func scanCandidate(row interface{ Scan(...any) error }, loc *time.Location) (Candidate, error) {
	var c Candidate
	var kind, created, moved string
	var interview sql.NullString
	err := row.Scan(&c.ID, &c.Token, &c.FirstName, &c.LastName, &c.Email, &c.Phone,
		&c.Apartment, &kind, &c.Message, &c.Reason, &c.ReasonNote,
		&c.Stage, &c.Responsible, &interview,
		&c.Note, &c.Source, &c.MemberID, &created, &c.CreatedIP, &moved, &c.MovedBy)
	if err != nil {
		return c, err
	}
	c.Kind = mustKind(kind)
	if interview.Valid && strings.TrimSpace(interview.String) != "" {
		t, err := ParseDay(interview.String, loc)
		if err != nil {
			return c, err
		}
		c.InterviewOn = sql.NullTime{Time: t, Valid: true}
	}
	if c.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return c, err
	}
	if c.MovedAt, err = time.Parse(time.RFC3339, moved); err != nil {
		return c, err
	}
	return c, nil
}
