package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// ProposalKind is what a proposal asks the board to do.
type ProposalKind string

const (
	// ProposeUpdate asks for a change to somebody already in the register.
	ProposeUpdate ProposalKind = "update"
	// ProposeDelete asks for somebody to be removed from it.
	ProposeDelete ProposalKind = "delete"
)

// ProposalStatus is where a proposal has got to.
type ProposalStatus string

const (
	// Pending is waiting for the board.
	Pending ProposalStatus = "pending"
	// Approved was carried out.
	Approved ProposalStatus = "approved"
	// Rejected was turned down.
	Rejected ProposalStatus = "rejected"
	// Withdrawn was taken back by whoever proposed it.
	Withdrawn ProposalStatus = "withdrawn"
	// Stale is what a proposal becomes when the member it concerns is gone,
	// or has been changed underneath it. Deciding it would either fail or
	// quietly undo somebody else's work, so it is retired instead.
	Stale ProposalStatus = "stale"
)

// Snapshot is a member as a proposal remembers them: everything a proposal
// may change, and nothing that only the database decides.
//
// It is stored as JSON rather than as columns because it is a record of what
// somebody asked for at a moment in time. Migrating it alongside the members
// table would rewrite history, which is the one thing an approval queue must
// not do.
type Snapshot struct {
	FirstName string      `json:"first_name"`
	LastName  string      `json:"last_name"`
	Email     string      `json:"email"`
	Phone     string      `json:"phone"`
	Kind      config.Kind `json:"kind"`
	Apartment string      `json:"apartment"`
	JoinedOn  string      `json:"joined_on"`
	LeftOn    string      `json:"left_on,omitempty"`
	// AlsoIn is comma-separated rather than a slice so that a Snapshot stays
	// comparable with ==, which is what tells the board that a member has
	// been changed underneath a waiting proposal.
	AlsoIn string `json:"also_in,omitempty"`
	Note   string `json:"note"`
}

// SnapshotOf captures a member.
func SnapshotOf(m Member) Snapshot {
	s := Snapshot{
		FirstName: m.FirstName,
		LastName:  m.LastName,
		Email:     m.Email,
		Phone:     m.Phone,
		Kind:      m.Kind,
		Apartment: m.Apartment,
		JoinedOn:  day(m.JoinedOn),
		AlsoIn:    encodeKinds(m.AlsoIn),
		Note:      m.Note,
	}
	if m.LeftOn.Valid {
		s.LeftOn = day(m.LeftOn.Time)
	}
	return s
}

// Apply writes a snapshot back over a member, leaving the bookkeeping columns
// alone. It returns an error rather than a mangled member if a date in the
// snapshot cannot be read.
func (s Snapshot) Apply(m Member, loc *time.Location) (Member, error) {
	joined, err := ParseDay(s.JoinedOn, loc)
	if err != nil {
		return m, err
	}
	m.FirstName, m.LastName = s.FirstName, s.LastName
	m.Email, m.Phone = Email(s.Email), s.Phone
	m.Kind, m.Apartment = s.Kind, s.Apartment
	m.AlsoIn = decodeKinds(s.AlsoIn)
	m.JoinedOn, m.Note = joined, s.Note
	m.LeftOn = sql.NullTime{}
	if s.LeftOn != "" {
		left, err := ParseDay(s.LeftOn, loc)
		if err != nil {
			return m, err
		}
		m.LeftOn = sql.NullTime{Time: left, Valid: true}
	}
	return m, nil
}

// Proposal is a change waiting for the board's blessing. Whoever answers the
// "I'd like to join" mail can write down a new member on the spot, but they
// cannot quietly change somebody's address or take them off the list — those
// go through here.
type Proposal struct {
	ID           string
	MemberID     string
	Kind         ProposalKind
	Before       Snapshot
	After        Snapshot
	Reason       string
	Status       ProposalStatus
	ProposedBy   string
	ProposedAt   time.Time
	DecidedBy    string
	DecidedAt    sql.NullTime
	DecisionNote string
}

// Open reports whether the proposal is still waiting.
func (p Proposal) Open() bool { return p.Status == Pending }

// CreateProposal files a proposal.
func (s *Store) CreateProposal(ctx context.Context, p Proposal) error {
	before, err := json.Marshal(p.Before)
	if err != nil {
		return err
	}
	after, err := json.Marshal(p.After)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO proposals (id, member_id, kind, before_json, after_json, reason,
			status, proposed_by, proposed_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		p.ID, p.MemberID, string(p.Kind), string(before), string(after), p.Reason,
		string(Pending), p.ProposedBy, utc(p.ProposedAt))
	return err
}

// DecideProposal records the board's answer. It only moves a proposal that is
// still pending, so two board members clicking at once cannot both decide it.
func (s *Store) DecideProposal(ctx context.Context, id string, status ProposalStatus, by, note string, at time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET status = ?, decided_by = ?, decided_at = ?, decision_note = ?
		WHERE id = ? AND status = ?`,
		string(status), by, utc(at), note, id, string(Pending))
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

// StaleProposalsFor retires every pending proposal about a member, for when
// the member is removed or edited out from under them.
func (s *Store) StaleProposalsFor(ctx context.Context, memberID string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE proposals SET status = ?, decided_at = ? WHERE member_id = ? AND status = ?`,
		string(Stale), utc(at), memberID, string(Pending))
	return err
}

// Proposal returns one by id.
func (s *Store) Proposal(ctx context.Context, id string) (Proposal, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+proposalCols+` FROM proposals WHERE id = ?`, id)
	p, err := scanProposal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PendingProposals returns everything waiting for the board, oldest first —
// the order they should be dealt with in.
func (s *Store) PendingProposals(ctx context.Context) ([]Proposal, error) {
	return s.queryProposals(ctx,
		`SELECT `+proposalCols+` FROM proposals WHERE status = ? ORDER BY proposed_at ASC`,
		string(Pending))
}

// DecidedProposals returns the most recently settled proposals, for the
// board's own record of what it has waved through.
func (s *Store) DecidedProposals(ctx context.Context, limit int) ([]Proposal, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queryProposals(ctx,
		`SELECT `+proposalCols+` FROM proposals WHERE status <> ?
		 ORDER BY COALESCE(decided_at, proposed_at) DESC LIMIT ?`,
		string(Pending), limit)
}

// ProposalsFor returns every proposal about one member, newest first.
func (s *Store) ProposalsFor(ctx context.Context, memberID string) ([]Proposal, error) {
	return s.queryProposals(ctx,
		`SELECT `+proposalCols+` FROM proposals WHERE member_id = ? ORDER BY proposed_at DESC`,
		memberID)
}

// CountPending is the number on the badge in the top bar.
func (s *Store) CountPending(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM proposals WHERE status = ?`, string(Pending)).Scan(&n)
	return n, err
}

const proposalCols = `id, member_id, kind, before_json, after_json, reason, status,
	proposed_by, proposed_at, decided_by, decided_at, decision_note`

func (s *Store) queryProposals(ctx context.Context, q string, args ...any) ([]Proposal, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Proposal
	for rows.Next() {
		p, err := scanProposal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanProposal(row interface{ Scan(...any) error }) (Proposal, error) {
	var p Proposal
	var kind, status, before, after, proposed string
	var decided sql.NullString
	err := row.Scan(&p.ID, &p.MemberID, &kind, &before, &after, &p.Reason, &status,
		&p.ProposedBy, &proposed, &p.DecidedBy, &decided, &p.DecisionNote)
	if err != nil {
		return p, err
	}
	p.Kind, p.Status = ProposalKind(kind), ProposalStatus(status)
	// A snapshot that will not parse is not worth failing the whole page for:
	// the proposal still shows who asked for what and when, which is what the
	// board is reading. Leave the fields empty and carry on.
	_ = json.Unmarshal([]byte(before), &p.Before)
	_ = json.Unmarshal([]byte(after), &p.After)
	if p.ProposedAt, err = time.Parse(time.RFC3339, proposed); err != nil {
		return p, err
	}
	if decided.Valid && decided.String != "" {
		t, err := time.Parse(time.RFC3339, decided.String)
		if err != nil {
			return p, err
		}
		p.DecidedAt = sql.NullTime{Time: t, Valid: true}
	}
	return p, nil
}
