package sync

import (
	"fmt"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/google"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// Grid lays the register out for the cashiers' spreadsheet.
//
// The shape is chosen for arithmetic rather than for reading: one row per
// member, one column per year of fees, amounts as bare numbers so that SUM
// works on them without anybody having to strip a "kr" first. Dates are
// written as ISO so that Sheets recognises them as dates whatever the
// spreadsheet's locale happens to be.
//
// The registry owns this tab and rewrites it whole on every pass. Anything
// the cashiers add here is lost within ten minutes; their own workings belong
// on a second tab that reads from this one.
func Grid(r membership.Roster, cfg *config.Config, now time.Time) google.Grid {
	loc := cfg.Location()
	now = now.In(loc)
	years := yearColumns(r, cfg, now)

	header := []any{
		"Namn", "Förnamn", "Efternamn", "E-post", "Telefon",
		"Medlemstyp", "Även med i", "Lägenhet", "Medlem sedan", "År som medlem", "Status",
		"Avgift i år", "Betalt i år", "Betaldatum",
	}
	for _, y := range years {
		header = append(header, fmt.Sprintf("%d", y))
	}
	header = append(header, "Anteckning")

	grid := google.Grid{header}

	for _, s := range r.All {
		m := s.Member
		row := []any{
			m.Name(),
			m.FirstName,
			m.LastName,
			m.Email,
			m.Phone,
			kindLabel(m.Kind),
			alsoLabel(m),
			m.Apartment,
			m.JoinedOn.In(loc).Format("2006-01-02"),
			s.Years,
			statusLabel(s),
			blankIfEnded(s, s.OwedKr),
			blankIfZero(s.PaidKr),
			paidOn(s, loc),
		}
		for _, y := range years {
			row = append(row, amountFor(s, y))
		}
		row = append(row, m.Note)
		grid = append(grid, row)
	}

	// A blank line and then a note, so that a cashier opening the tab in June
	// can tell at a glance whether they are looking at something fresh or at
	// a synchronisation that has been broken since Easter. SUM and AVERAGE
	// skip both rows; COUNTA over a whole column will see the note, which is
	// why the workings belong on their own tab.
	grid = append(grid, []any{})
	grid = append(grid, []any{
		"Skrivet av " + cfg.Site.Title,
		now.Format("2006-01-02 15:04"),
		"Ändra inget här — fliken skrivs om automatiskt.",
	})
	return grid
}

// yearColumns are the fee years worth a column: this one, and back as far as
// the configuration asks or the register goes, whichever is shorter.
func yearColumns(r membership.Roster, cfg *config.Config, now time.Time) []int {
	want := cfg.Sheet.YearColumns()
	earliest := now.Year()
	for _, s := range r.All {
		if y := s.Member.JoinedOn.Year(); y < earliest {
			earliest = y
		}
		for _, y := range s.PaidYears {
			if y < earliest {
				earliest = y
			}
		}
	}
	if now.Year()-earliest+1 < want {
		want = now.Year() - earliest + 1
	}
	if want < 1 {
		want = 1
	}
	out := make([]int, 0, want)
	for y := now.Year(); len(out) < want; y-- {
		out = append(out, y)
	}
	return out
}

// amountFor is what a member paid in a year, as a bare number so the column
// sums. A blank cell means nothing was received; a zero means the fee was
// recorded as waived, and the two are not the same thing to a cashier.
func amountFor(s membership.Status, year int) any {
	if kr, ok := s.PaidByYear[year]; ok {
		return kr
	}
	return ""
}

// alsoLabel names the extra groups a member has been added to, for the
// cashiers' sheet — it is the column that explains why a bomedlem turns up on
// the vänmedlemmarnas utskick.
func alsoLabel(m store.Member) string {
	var out []string
	for _, k := range m.AlsoIn {
		out = append(out, kindLabel(k))
	}
	return strings.Join(out, ", ")
}

func kindLabel(k config.Kind) string {
	if k == config.KindBo {
		return "Bomedlem"
	}
	return "Vänmedlem"
}

func statusLabel(s membership.Status) string {
	if !s.Current() {
		return "Utträdd"
	}
	switch s.Fee {
	case membership.Paid:
		return "Betalt"
	case membership.Overdue:
		return "FÖRFALLEN"
	default:
		return "Väntar på betalning"
	}
}

func paidOn(s membership.Status, loc *time.Location) any {
	if !s.HasPaid {
		return ""
	}
	return s.Payment.PaidOn.In(loc).Format("2006-01-02")
}

func blankIfZero(n int) any {
	if n == 0 {
		return ""
	}
	return n
}

func blankIfEnded(s membership.Status, n int) any {
	if !s.Current() {
		return ""
	}
	return n
}
