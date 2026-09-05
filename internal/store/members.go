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
	LeftOn sql.NullTime
	// AlsoIn are the groups this member belongs to beyond their own kind.
	//
	// A bomedlem who runs a matlag or arranges spelkvällar has to be able to
	// write to the vänmedlemmar, and a Google group only accepts post from
	// somebody who is in it. Before this was a column it was a hand-kept list
	// in the configuration file that nobody could see and nobody updated.
	AlsoIn    []config.Kind
	Note      string
	CreatedAt time.Time
	CreatedBy string
	UpdatedAt time.Time
	UpdatedBy string
}

// Name is how the member is written on a page and in a contact card.
func (m Member) Name() string { return joinName(m.FirstName, m.LastName) }

// joinName puts a first and last name together, tolerating either being
// missing — plenty of contact cards carry only one.
func joinName(first, last string) string {
	return strings.TrimSpace(strings.TrimSpace(first) + " " + strings.TrimSpace(last))
}

// Current reports whether the membership is still running.
func (m Member) Current() bool { return !m.LeftOn.Valid }

// In reports whether the member belongs in the group for a kind: either it is
// their own kind, or they have been added to it as well.
func (m Member) In(k config.Kind) bool {
	if m.Kind == k {
		return true
	}
	for _, extra := range m.AlsoIn {
		if extra == k {
			return true
		}
	}
	return false
}

// Extra reports whether the member is in a kind's group only because somebody
// put them there, which is what a page marks with "även med i".
func (m Member) Extra(k config.Kind) bool { return m.Kind != k && m.In(k) }

// encodeKinds and decodeKinds move the extra memberships in and out of their
// column. Comma-separated rather than a table of its own: there are two kinds
// and a member has at most one extra, so a join would be ceremony.
func encodeKinds(kinds []config.Kind) string {
	seen := map[config.Kind]bool{}
	var out []string
	for _, k := range kinds {
		if k.Valid() && !seen[k] {
			seen[k] = true
			out = append(out, string(k))
		}
	}
	sort.Strings(out)
	return strings.Join(out, ",")
}

func decodeKinds(raw string) []config.Kind {
	var out []config.Kind
	for _, part := range strings.Split(raw, ",") {
		if k, ok := config.ParseKind(part); ok {
			out = append(out, k)
		}
	}
	return out
}

// SortName is what an alphabetical list orders on: surname first, the way a
// register has been sorted since long before there were computers.
func (m Member) SortName() string {
	return strings.ToLower(strings.TrimSpace(m.LastName) + " " + strings.TrimSpace(m.FirstName))
}

const memberCols = `id, first_name, last_name, email, phone, kind, apartment,
	joined_on, left_on, also_in, note, created_at, created_by, updated_at, updated_by`

// scanMember reads one row. loc is the association's timezone: the day
// columns hold days, and a day only means something in a place.
func scanMember(row interface{ Scan(...any) error }, loc *time.Location) (Member, error) {
	var m Member
	var kind, joined, alsoIn, created, updated string
	var left sql.NullString
	err := row.Scan(&m.ID, &m.FirstName, &m.LastName, &m.Email, &m.Phone, &kind, &m.Apartment,
		&joined, &left, &alsoIn, &m.Note, &created, &m.CreatedBy, &updated, &m.UpdatedBy)
	if err != nil {
		return m, err
	}
	m.Kind = mustKind(kind)
	m.AlsoIn = decodeKinds(alsoIn)
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
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		m.ID, m.FirstName, m.LastName, m.Email, m.Phone, string(m.Kind), m.Apartment,
		day(m.JoinedOn), leftValue(m), encodeKinds(m.AlsoIn), m.Note,
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
			joined_on=?, left_on=?, also_in=?, note=?, updated_at=?, updated_by=?
		WHERE id=?`,
		m.FirstName, m.LastName, m.Email, m.Phone, string(m.Kind), m.Apartment,
		day(m.JoinedOn), leftValue(m), encodeKinds(m.AlsoIn), m.Note,
		utc(m.UpdatedAt), m.UpdatedBy, m.ID)
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
