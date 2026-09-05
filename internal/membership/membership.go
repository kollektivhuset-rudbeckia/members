// Package membership answers the questions the register exists to answer:
// how long has this person been a member, and are they square with the
// association for the year.
//
// None of it touches the database or a template. It is the one place where
// the association's own rules are written down, so that the page, the
// spreadsheet and the overdue list cannot each have their own idea of what
// "overdue" means.
package membership

import (
	"sort"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// FeeState is where a member stands on this year's fee.
type FeeState string

const (
	// Paid means the year's fee is recorded against them.
	Paid FeeState = "paid"
	// Due means it is not, but the day it has to be in by has not passed.
	// A member who joined last week is Due, not Overdue, whatever the date.
	Due FeeState = "due"
	// Overdue means the day has passed and nothing has landed. This is what
	// the cashiers chase and what the register nags about.
	Overdue FeeState = "overdue"
	// Ended means the membership is over, so no fee is expected.
	Ended FeeState = "ended"
)

// Late reports whether somebody should be chased.
func (f FeeState) Late() bool { return f == Overdue }

// Status is everything derived about one member at one moment: their standing
// on the fee, how long they have been a member, and which years they have
// paid for. It is computed once per request and handed to the template, so no
// template ever does arithmetic.
type Status struct {
	Member store.Member
	// Year is the year all of this is reckoned against.
	Year int
	Fee  FeeState
	// DueBy is the day this year's fee has to be in by for this member. It is
	// the association's due date, plus its grace, but never sooner than a new
	// member's own allowance from the day they joined.
	DueBy time.Time
	// Payment is this year's fee if there is one.
	Payment store.Payment
	HasPaid bool
	PaidKr  int
	// OwedKr is what the member still owes, across every year the register is
	// answerable for — not only this one.
	OwedKr    int
	PaidYears []int
	// PaidByYear is the amount received for each year, which is what the
	// cashiers add up. A year with a payment of zero kronor is still a paid
	// year — a fee can be waived — so presence in the map, not the amount,
	// is what "paid" means.
	PaidByYear map[int]int
	// Unpaid are the years whose fee never arrived and whose last date has
	// passed, oldest first.
	//
	// Looking at more than the current year is what stops two people slipping
	// through: somebody written down in November, whose own allowance runs
	// out in January when the register has already moved on to the next year,
	// and somebody who quietly missed a year three years ago and has paid
	// every year since.
	Unpaid []int
	// Years and Months are how long they have been a member, as of now.
	Years  int
	Months int
	// Days is the same in whole days, for sorting a column by tenure without
	// the years-and-months rounding making neighbours compare equal.
	Days int
}

// Current reports whether the membership is still running.
func (s Status) Current() bool { return s.Member.Current() }

// Compute derives everything about one member.
func Compute(m store.Member, payments []store.Payment, cfg *config.Config, now time.Time) Status {
	loc := cfg.Location()
	now = now.In(loc)
	year := now.Year()

	st := Status{Member: m, Year: year}

	// Tenure is measured to the day they left, if they have. A former member
	// of eleven years should still read as eleven years, not as however long
	// ago that was.
	until := now
	if m.LeftOn.Valid {
		until = m.LeftOn.Time
	}
	st.Years, st.Months = span(m.JoinedOn, until)
	st.Days = int(dayStart(until, loc).Sub(dayStart(m.JoinedOn, loc)).Hours() / 24)
	if st.Days < 0 {
		st.Days = 0
	}

	st.PaidByYear = make(map[int]int, len(payments))
	for _, p := range payments {
		st.PaidYears = append(st.PaidYears, p.Year)
		st.PaidByYear[p.Year] = p.AmountKr
		if p.Year == year {
			st.Payment, st.HasPaid, st.PaidKr = p, true, p.AmountKr
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(st.PaidYears)))

	st.DueBy = DueBy(m, cfg, year)

	// Each year from the first one the register can answer for, up to this
	// one. A year counts as outstanding only once its own last date has gone
	// by — every year has its own, because a member who joined in that year
	// gets their allowance counted from the day they were written down.
	for y := chaseFrom(m, cfg, year, loc); y <= year; y++ {
		if _, ok := st.PaidByYear[y]; ok {
			continue
		}
		if now.After(endOfDay(DueBy(m, cfg, y), loc)) {
			st.Unpaid = append(st.Unpaid, y)
			st.OwedKr += cfg.Membership.FeeFor(m.Kind)
		} else if y == year {
			// Not yet due, but still expected before the year is out.
			st.OwedKr += cfg.Membership.FeeFor(m.Kind)
		}
	}

	switch {
	case !m.Current():
		// A membership that has ended settles nothing further. Whatever was
		// owed when they left is a matter for the minutes, not a red badge.
		st.Fee, st.OwedKr, st.Unpaid = Ended, 0, nil
	case len(st.Unpaid) > 0:
		st.Fee = Overdue
	case st.HasPaid:
		st.Fee = Paid
	default:
		st.Fee = Due
	}
	return st
}

// chaseFrom is the earliest year a member can be held to account for.
//
// Three things bound it, and each is there to stop a different piece of
// nonsense:
//
//   - the year they joined, because nobody owes a fee for a year before they
//     were a member;
//   - the year their row was created, because the register cannot vouch for a
//     payment made before it existed. This is what stops importing somebody
//     who joined in 2014 from inventing eleven years of debt on the spot;
//   - last year, because reaching further back turns a register that has been
//     kept casually into a wall of red. Last year is included rather than only
//     this one so that a member written down in November — whose own allowance
//     runs out in January, when the register has already moved on — does not
//     slip through the crack at the turn of the year.
//
// An association that does want the full history says so with
// membership.chase_from_year, which overrides the last of the three.
func chaseFrom(m store.Member, cfg *config.Config, year int, loc *time.Location) int {
	from := year - 1
	if set := cfg.Membership.ChaseFromYear; set > 0 {
		from = set
	}
	if joined := m.JoinedOn.In(loc).Year(); joined > from {
		from = joined
	}
	if created := m.CreatedAt.In(loc).Year(); created > from {
		from = created
	}
	return from
}

// Owing is the oldest year still outstanding, which is the one to chase.
func (s Status) Owing() int {
	if len(s.Unpaid) == 0 {
		return 0
	}
	return s.Unpaid[0]
}

// DueBy is the day a member's fee for a year has to be in by.
//
// It is the later of two dates, and the second one is the whole reason this
// function exists: somebody written down in the middle of November has not
// missed a due date in March. They get their own allowance, so that adding a
// member the moment they show interest never puts them in the red the same
// afternoon.
//
// The allowance runs from the day the register learned about them, which is
// not always the day they joined. A member imported from the old contact list
// may have joined in 2014, and chasing them for a date that went by in April
// on the strength of a row created in June would be the register blaming
// somebody for its own late arrival.
func DueBy(m store.Member, cfg *config.Config, year int) time.Time {
	loc := cfg.Location()
	club := cfg.Membership.DueDate(year, loc).AddDate(0, 0, cfg.Membership.GraceDays)
	own := knownSince(m, loc).AddDate(0, 0, cfg.Membership.NewMemberDays)
	if own.After(club) {
		return own
	}
	return club
}

// knownSince is the day the register first had this member on its books.
func knownSince(m store.Member, loc *time.Location) time.Time {
	joined := m.JoinedOn.In(loc)
	created := m.CreatedAt.In(loc)
	if created.After(joined) {
		return created
	}
	return joined
}

// span is how long between two days, in whole years and leftover months.
func span(from, to time.Time) (years, months int) {
	if to.Before(from) {
		return 0, 0
	}
	years = to.Year() - from.Year()
	months = int(to.Month()) - int(from.Month())
	if to.Day() < from.Day() {
		months--
	}
	if months < 0 {
		years--
		months += 12
	}
	if years < 0 {
		return 0, 0
	}
	return years, months
}

func dayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func endOfDay(t time.Time, loc *time.Location) time.Time {
	return dayStart(t, loc).AddDate(0, 0, 1).Add(-time.Nanosecond)
}

// Roster is the whole register with everything derived, ready to be filtered,
// sorted and counted. Building it is one pass over two reads.
type Roster struct {
	All []Status
	Now time.Time
	Cfg *config.Config
}

// Build derives the status of every member.
func Build(members []store.Member, payments map[string][]store.Payment, cfg *config.Config, now time.Time) Roster {
	out := make([]Status, 0, len(members))
	for _, m := range members {
		out = append(out, Compute(m, payments[m.ID], cfg, now))
	}
	return Roster{All: out, Now: now, Cfg: cfg}
}

// Counts is the handful of numbers across the top of the register.
type Counts struct {
	Bo      int
	Van     int
	Current int
	Former  int
	Paid    int
	Overdue int
	// OwedKr is what the association is still waiting for this year.
	OwedKr int
}

// Count summarises a roster.
func (r Roster) Count() Counts {
	var c Counts
	for _, s := range r.All {
		if !s.Current() {
			c.Former++
			continue
		}
		c.Current++
		switch s.Member.Kind {
		case config.KindBo:
			c.Bo++
		case config.KindVan:
			c.Van++
		}
		switch s.Fee {
		case Paid:
			c.Paid++
		case Overdue:
			c.Overdue++
		}
		c.OwedKr += s.OwedKr
	}
	return c
}

// Overdue returns the members who should be chased, longest overdue first.
func (r Roster) Overdue() []Status {
	var out []Status
	for _, s := range r.All {
		if s.Fee == Overdue {
			out = append(out, s)
		}
	}
	// Longest-standing debt first: the year they owe from, then how long ago
	// that year's date went by.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Owing() != out[j].Owing() {
			return out[i].Owing() < out[j].Owing()
		}
		return out[i].DueBy.Before(out[j].DueBy)
	})
	return out
}

// Filter narrows a register down to what somebody asked to see.
type Filter struct {
	// Kind limits to one sort of membership. Empty means both.
	Kind config.Kind
	// Fee limits to one standing on this year's fee. Empty means any.
	Fee FeeState
	// Show is "current" (the default), "former" or "all".
	Show string
	// Query matches a name, an address, a phone number or an apartment.
	Query string
}

// Empty reports whether the filter would let everything through, so the page
// can leave the "clear" button out when there is nothing to clear.
func (f Filter) Empty() bool {
	return f.Kind == "" && f.Fee == "" && f.Query == "" && (f.Show == "" || f.Show == "current")
}

// Apply returns the members matching f.
func (r Roster) Apply(f Filter) []Status {
	needle := strings.ToLower(strings.TrimSpace(f.Query))
	var out []Status
	for _, s := range r.All {
		switch f.Show {
		case "former":
			if s.Current() {
				continue
			}
		case "all":
		default:
			if !s.Current() {
				continue
			}
		}
		if f.Kind != "" && s.Member.Kind != f.Kind {
			continue
		}
		if f.Fee != "" && s.Fee != f.Fee {
			continue
		}
		if needle != "" && !matches(s.Member, needle) {
			continue
		}
		out = append(out, s)
	}
	return out
}

// matches is the search behind the box above the table. Several words all
// have to match something, so "anna 14" finds Anna in apartment 1403 and not
// every Anna in the house.
func matches(m store.Member, needle string) bool {
	hay := strings.ToLower(strings.Join([]string{
		m.FirstName, m.LastName, m.Email, m.Phone, m.Apartment, m.Note,
	}, " "))
	for _, word := range strings.Fields(needle) {
		if !strings.Contains(hay, word) {
			return false
		}
	}
	return true
}

// Sort orders a list of members by one of the columns. Unknown keys fall back
// to the register's own order, surname first, so a hand-typed URL cannot
// produce an unsorted page.
func Sort(list []Status, key string, descending bool) {
	less := func(i, j int) bool { return list[i].Member.SortName() < list[j].Member.SortName() }
	switch key {
	case "email":
		less = func(i, j int) bool {
			return strings.ToLower(list[i].Member.Email) < strings.ToLower(list[j].Member.Email)
		}
	case "kind":
		less = func(i, j int) bool {
			if list[i].Member.Kind != list[j].Member.Kind {
				return list[i].Member.Kind < list[j].Member.Kind
			}
			return list[i].Member.SortName() < list[j].Member.SortName()
		}
	case "apartment":
		less = func(i, j int) bool {
			// An empty apartment sorts last rather than first: a vänmedlem
			// has none, and a column of blanks at the top helps nobody.
			a, b := list[i].Member.Apartment, list[j].Member.Apartment
			if (a == "") != (b == "") {
				return b == ""
			}
			return a < b
		}
	case "joined":
		less = func(i, j int) bool { return list[i].Member.JoinedOn.Before(list[j].Member.JoinedOn) }
	case "tenure":
		less = func(i, j int) bool { return list[i].Days < list[j].Days }
	case "fee":
		// Worst first: overdue, then due, then paid, then ended. Sorting by
		// the word would put "betald" before "obetald" and bury the problem.
		rank := map[FeeState]int{Overdue: 0, Due: 1, Paid: 2, Ended: 3}
		less = func(i, j int) bool {
			if rank[list[i].Fee] != rank[list[j].Fee] {
				return rank[list[i].Fee] < rank[list[j].Fee]
			}
			return list[i].Member.SortName() < list[j].Member.SortName()
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if descending {
			return less(j, i)
		}
		return less(i, j)
	})
}

// SortKeys are the columns the table can be ordered by. The page builds its
// header links from this, so a new column cannot be sortable in the template
// and unsortable in the code.
var SortKeys = []string{"name", "email", "kind", "apartment", "joined", "tenure", "fee"}

// ValidSort narrows a query parameter to a column that exists.
func ValidSort(key string) string {
	for _, k := range SortKeys {
		if k == key {
			return k
		}
	}
	return "name"
}
