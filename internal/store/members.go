package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// Member is one person in the register.
//
// There is no "prospect" state. Somebody is written down the moment they show
// interest and goes into their Google group straight away — waiting for the
// money to land before letting them hear from the house is exactly backwards.
// Whether they have paid is a separate question, answered by the payments.
type Member struct {
	ID        string
	FirstName string
	LastName  string
	Email     string
	Phone     string
	Kind      config.Kind
	// Apartment is only meaningful for a bomedlem, and even then it is
	// optional: somebody can join before they have moved in.
	Apartment string
	// JoinedOn is the day the association counts them from. It is what
	// tenure is measured against, not the day the first fee was paid.
	JoinedOn time.Time
	// LeftOn is set when a membership ends. A former member keeps their row —
	// the association wants to know it had them — but leaves every group.
	LeftOn    sql.NullTime
	Note      string
	CreatedAt time.Time
	CreatedBy string
	UpdatedAt time.Time
	UpdatedBy string
}

// Name is how the member is written on a page and in a contact card.
func (m Member) Name() string {
	return strings.TrimSpace(strings.TrimSpace(m.FirstName) + " " + strings.TrimSpace(m.LastName))
}

// Current reports whether the membership is still running.
func (m Member) Current() bool { return !m.LeftOn.Valid }

// SortName is what an alphabetical list orders on: surname first, the way a
// register has been sorted since long before there were computers.
func (m Member) SortName() string {
	return strings.ToLower(strings.TrimSpace(m.LastName) + " " + strings.TrimSpace(m.FirstName))
}

const memberCols = `id, first_name, last_name, email, phone, kind, apartment,
	joined_on, left_on, note, created_at, created_by, updated_at, updated_by`

// scanMember reads one row. loc is the association's timezone: the day
// columns hold days, and a day only means something in a place.
func scanMember(row interface{ Scan(...any) error }, loc *time.Location) (Member, error) {
	var m Member
	var kind, joined, created, updated string
	var left sql.NullString
	err := row.Scan(&m.ID, &m.FirstName, &m.LastName, &m.Email, &m.Phone, &kind, &m.Apartment,
		&joined, &left, &m.Note, &created, &m.CreatedBy, &updated, &m.UpdatedBy)
	if err != nil {
		return m, err
	}
	m.Kind = mustKind(kind)
	if m.JoinedOn, err = ParseDay(joined, loc); err != nil {
		return m, fmt.Errorf("member %s has an unreadable joined_on %q: %w", m.ID, joined, err)
	}
	if left.Valid && strings.TrimSpace(left.String) != "" {
		t, err := ParseDay(left.String, loc)
		if err != nil {
			return m, fmt.Errorf("member %s has an unreadable left_on %q: %w", m.ID, left.String, err)
		}
		m.LeftOn = sql.NullTime{Time: t, Valid: true}
	}
	if m.CreatedAt, err = time.Parse(time.RFC3339, created); err != nil {
		return m, err
	}
	if m.UpdatedAt, err = time.Parse(time.RFC3339, updated); err != nil {
		return m, err
	}
	return m, nil
}

// leftValue turns the optional leaving day into what the column holds.
func leftValue(m Member) any {
	if !m.LeftOn.Valid {
		return nil
	}
	return day(m.LeftOn.Time)
}

// CreateMember writes a new member. The address must be free.
func (s *Store) CreateMember(ctx context.Context, m Member) error {
	m.Email = Email(m.Email)
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO members (`+memberCols+`)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.FirstName, m.LastName, m.Email, m.Phone, string(m.Kind), m.Apartment,
		day(m.JoinedOn), leftValue(m), m.Note,
		utc(m.CreatedAt), m.CreatedBy, utc(m.UpdatedAt), m.UpdatedBy)
	if isUnique(err) {
		return ErrDuplicateEmail
	}
	return err
}

// UpdateMember overwrites everything about a member except who created them
// and when. The caller has already decided this is allowed.
func (s *Store) UpdateMember(ctx context.Context, m Member) error {
	m.Email = Email(m.Email)
	res, err := s.db.ExecContext(ctx, `
		UPDATE members SET first_name=?, last_name=?, email=?, phone=?, kind=?, apartment=?,
			joined_on=?, left_on=?, note=?, updated_at=?, updated_by=?
		WHERE id=?`,
		m.FirstName, m.LastName, m.Email, m.Phone, string(m.Kind), m.Apartment,
		day(m.JoinedOn), leftValue(m), m.Note, utc(m.UpdatedAt), m.UpdatedBy, m.ID)
	if isUnique(err) {
		return ErrDuplicateEmail
	}
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

// DeleteMember removes a member and everything hanging off them. The audit
// trail keeps the fact that they were here and who removed them, which is the
// part the association actually needs afterwards.
func (s *Store) DeleteMember(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM members WHERE id = ?`, id)
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

// Member returns one member by id.
func (s *Store) Member(ctx context.Context, id string, loc *time.Location) (Member, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE id = ?`, id)
	m, err := scanMember(row, loc)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// MemberByEmail returns one member by address, which is the key everything
// synchronises on.
func (s *Store) MemberByEmail(ctx context.Context, email string, loc *time.Location) (Member, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+memberCols+` FROM members WHERE email = ?`, Email(email))
	m, err := scanMember(row, loc)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// Members returns every member, surname first. The register is a few hundred
// people at most, so it is read whole and sorted, filtered and counted in
// memory rather than in ever more elaborate SQL.
func (s *Store) Members(ctx context.Context, loc *time.Location) ([]Member, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+memberCols+` FROM members`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		m, err := scanMember(rows, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SortName() < out[j].SortName() })
	return out, nil
}

// CountMembers returns how many rows the register holds, for the demo seeder
// and the startup log.
func (s *Store) CountMembers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM members`).Scan(&n)
	return n, err
}
