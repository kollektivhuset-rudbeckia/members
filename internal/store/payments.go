package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Payment is one year's fee, received. The fee runs by calendar year, so a
// member has at most one payment per year: if it arrives in two instalments
// the cashier corrects the amount rather than adding a second row, and the
// audit trail keeps the correction.
type Payment struct {
	MemberID     string
	Year         int
	AmountKr     int
	PaidOn       time.Time
	Method       string
	Note         string
	RegisteredBy string
	RegisteredAt time.Time
}

// RecordPayment writes or corrects the fee for one year.
func (s *Store) RecordPayment(ctx context.Context, p Payment) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO payments (member_id, year, amount_kr, paid_on, method, note, registered_by, registered_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(member_id, year) DO UPDATE SET
			amount_kr = excluded.amount_kr,
			paid_on = excluded.paid_on,
			method = excluded.method,
			note = excluded.note,
			registered_by = excluded.registered_by,
			registered_at = excluded.registered_at`,
		p.MemberID, p.Year, p.AmountKr, day(p.PaidOn), p.Method, p.Note,
		p.RegisteredBy, utc(p.RegisteredAt))
	return err
}

// WithdrawPayment takes a year's fee back off the record, for the ordinary
// case of a cashier ticking the wrong row.
func (s *Store) WithdrawPayment(ctx context.Context, memberID string, year int) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM payments WHERE member_id = ? AND year = ?`, memberID, year)
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

// Payment returns one year's fee for one member.
func (s *Store) Payment(ctx context.Context, memberID string, year int, loc *time.Location) (Payment, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT member_id, year, amount_kr, paid_on, method, note, registered_by, registered_at
		FROM payments WHERE member_id = ? AND year = ?`, memberID, year)
	p, err := scanPayment(row, loc)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// PaymentsFor returns one member's fees, newest year first.
func (s *Store) PaymentsFor(ctx context.Context, memberID string, loc *time.Location) ([]Payment, error) {
	return s.queryPayments(ctx, loc, `
		SELECT member_id, year, amount_kr, paid_on, method, note, registered_by, registered_at
		FROM payments WHERE member_id = ? ORDER BY year DESC`, memberID)
}

// AllPayments returns every fee ever recorded, keyed by member. The register
// is small enough that the payment page, the spreadsheet and the overdue list
// all work off one read rather than a query per member.
func (s *Store) AllPayments(ctx context.Context, loc *time.Location) (map[string][]Payment, error) {
	list, err := s.queryPayments(ctx, loc, `
		SELECT member_id, year, amount_kr, paid_on, method, note, registered_by, registered_at
		FROM payments ORDER BY year DESC`)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]Payment, len(list))
	for _, p := range list {
		out[p.MemberID] = append(out[p.MemberID], p)
	}
	return out, nil
}

func (s *Store) queryPayments(ctx context.Context, loc *time.Location, q string, args ...any) ([]Payment, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Payment
	for rows.Next() {
		p, err := scanPayment(rows, loc)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func scanPayment(row interface{ Scan(...any) error }, loc *time.Location) (Payment, error) {
	var p Payment
	var paid, registered string
	err := row.Scan(&p.MemberID, &p.Year, &p.AmountKr, &paid, &p.Method, &p.Note,
		&p.RegisteredBy, &registered)
	if err != nil {
		return p, err
	}
	if p.PaidOn, err = ParseDay(paid, loc); err != nil {
		return p, err
	}
	if p.RegisteredAt, err = time.Parse(time.RFC3339, registered); err != nil {
		return p, err
	}
	return p, nil
}
