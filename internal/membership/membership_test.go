package membership

import (
	"database/sql"
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

var stockholm = mustLoad("Europe/Stockholm")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// testConfig is the association as the tests assume it: 200 kr a year, due
// at the end of March, a fortnight's grace, and 45 days for somebody new.
// It goes through the real parser, so a test cannot assume a shape the
// configuration file could not actually produce.
func testConfig() *config.Config {
	cfg, err := config.Parse([]byte(`
site:
  title: Test
  language: sv
  timezone: Europe/Stockholm
membership:
  fee_kr: 200
  due_on: "03-31"
  grace_days: 14
  new_member_days: 45
groups:
  - kind: bo
    email: bo@example.test
  - kind: van
    email: van@example.test
`))
	if err != nil {
		panic(err)
	}
	return cfg
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, stockholm)
}

func member(joined time.Time) store.Member {
	return store.Member{
		ID: "m1", FirstName: "Anna", LastName: "Andersson",
		Email: "anna@example.test", Kind: config.KindBo, JoinedOn: joined,
		// Written down when they joined, which is the ordinary case. The
		// created date is one of the things that bounds how far back the
		// register will chase, so a test that leaves it at zero is testing
		// something no real member looks like.
		CreatedAt: joined,
	}
}

// paid records a fee for each year given.
func paid(years ...int) []store.Payment {
	var out []store.Payment
	for _, y := range years {
		out = append(out, store.Payment{Year: y, AmountKr: 200, PaidOn: day(y, time.February, 1)})
	}
	return out
}

func TestFeeStateFollowsTheDueDate(t *testing.T) {
	cfg := testConfig()
	old := member(day(2015, time.June, 1))

	tests := []struct {
		name     string
		now      time.Time
		payments []store.Payment
		want     FeeState
	}{
		{"paid for this year", day(2026, time.June, 1), paid(2025, 2026), Paid},
		{"unpaid but the due date has not passed", day(2026, time.February, 1), paid(2025), Due},
		{"unpaid on the due date itself", day(2026, time.April, 14), paid(2025), Due},
		{"unpaid the day after the grace runs out", day(2026, time.April, 15), paid(2025), Overdue},
		{"last year's payment does not count for this one", day(2026, time.June, 1), paid(2025), Overdue},
		{"a year missed last year is chased too", day(2026, time.June, 1), paid(2026), Overdue},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Compute(old, tc.payments, cfg, tc.now).Fee
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The whole point of being able to write somebody down the moment they show
// interest is that doing so must not put them in the red the same afternoon.
func TestANewMemberGetsTheirOwnAllowance(t *testing.T) {
	cfg := testConfig()

	// Somebody written down in November, long after March's due date.
	fresh := member(day(2026, time.November, 20))

	if got := Compute(fresh, nil, cfg, day(2026, time.November, 20)).Fee; got != Due {
		t.Errorf("on the day they joined: got %q, want %q", got, Due)
	}
	if got := Compute(fresh, nil, cfg, day(2026, time.December, 20)).Fee; got != Due {
		t.Errorf("a month later, still inside the 45 days: got %q, want %q", got, Due)
	}
	// 45 days from 20 November is 4 January — the allowance carries past the
	// turn of the year rather than expiring with it.
	if got := Compute(fresh, nil, cfg, day(2027, time.January, 10)).Fee; got != Overdue {
		t.Errorf("well past the allowance: got %q, want %q", got, Overdue)
	}
}

func TestDueByIsTheLaterOfTheTwoDates(t *testing.T) {
	cfg := testConfig()

	// An old member is governed by the association's date plus its grace.
	old := member(day(2015, time.June, 1))
	if got, want := DueBy(old, cfg, 2026), day(2026, time.April, 14); !got.Equal(want) {
		t.Errorf("an established member: got %s, want %s", got, want)
	}

	// Somebody who joined in March gets their own 45 days, which run past it.
	recent := member(day(2026, time.March, 20))
	if got, want := DueBy(recent, cfg, 2026), day(2026, time.May, 4); !got.Equal(want) {
		t.Errorf("a member who joined in March: got %s, want %s", got, want)
	}
}

func TestAFormerMemberKeepsTheirTenureAndOwesNothing(t *testing.T) {
	cfg := testConfig()
	m := member(day(2010, time.April, 1))
	m.LeftOn = sql.NullTime{Time: day(2021, time.April, 1), Valid: true}

	st := Compute(m, paid(2020), cfg, day(2026, time.June, 1))
	if st.Fee != Ended {
		t.Errorf("fee state: got %q, want %q", st.Fee, Ended)
	}
	// Eleven years a member, and still eleven years five years after leaving.
	if st.Years != 11 {
		t.Errorf("tenure: got %d years, want 11", st.Years)
	}
}

func TestTenureCountsWholeYearsAndMonths(t *testing.T) {
	cfg := testConfig()
	tests := []struct {
		joined        time.Time
		now           time.Time
		years, months int
	}{
		{day(2020, time.January, 15), day(2026, time.January, 15), 6, 0},
		{day(2020, time.January, 15), day(2026, time.January, 14), 5, 11},
		{day(2026, time.January, 15), day(2026, time.March, 20), 0, 2},
		{day(2026, time.September, 1), day(2026, time.September, 15), 0, 0},
		// A member written down tomorrow — the form allows a day's slack for
		// somebody filling it in late at night — is not negative years old.
		{day(2026, time.September, 20), day(2026, time.September, 19), 0, 0},
	}
	for _, tc := range tests {
		st := Compute(member(tc.joined), nil, cfg, tc.now)
		if st.Years != tc.years || st.Months != tc.months {
			t.Errorf("joined %s, now %s: got %dy %dm, want %dy %dm",
				tc.joined.Format("2006-01-02"), tc.now.Format("2006-01-02"),
				st.Years, st.Months, tc.years, tc.months)
		}
	}
}

func TestPaidByYearDistinguishesAWaivedFeeFromNoFee(t *testing.T) {
	cfg := testConfig()
	waived := append(paid(2025),
		store.Payment{Year: 2026, AmountKr: 0, PaidOn: day(2026, time.March, 1)})
	st := Compute(member(day(2020, time.January, 1)), waived, cfg, day(2026, time.June, 1))

	if st.Fee != Paid {
		t.Errorf("a waived fee is still a paid year: got %q", st.Fee)
	}
	if kr, ok := st.PaidByYear[2026]; !ok || kr != 0 {
		t.Errorf("PaidByYear[2026] = %d, %v; want 0, true", kr, ok)
	}
	if _, ok := st.PaidByYear[2024]; ok {
		t.Error("PaidByYear has a year nobody paid for")
	}
}

func roster(t *testing.T) Roster {
	t.Helper()
	cfg := testConfig()
	now := day(2026, time.June, 1)

	anna := store.Member{ID: "a", FirstName: "Anna", LastName: "Andersson",
		Email: "anna@example.test", Kind: config.KindBo, Apartment: "1403",
		JoinedOn: day(2015, time.March, 1), CreatedAt: day(2015, time.March, 1)}
	bo := store.Member{ID: "b", FirstName: "Bo", LastName: "Bengtsson",
		Email: "bo@example.test", Kind: config.KindVan,
		JoinedOn: day(2024, time.January, 1), CreatedAt: day(2024, time.January, 1)}
	cecilia := store.Member{ID: "c", FirstName: "Cecilia", LastName: "Dahl",
		Email: "cecilia@example.test", Kind: config.KindBo, Apartment: "0201",
		JoinedOn: day(2020, time.January, 1), CreatedAt: day(2020, time.January, 1),
		LeftOn: sql.NullTime{Time: day(2025, time.June, 1), Valid: true}}

	// Anna is square. Bo paid last year and owes this one. Cecilia has left.
	return Build([]store.Member{anna, bo, cecilia}, map[string][]store.Payment{
		"a": paid(2025, 2026),
		"b": paid(2025),
	}, cfg, now)
}

func TestFilterDefaultsToCurrentMembers(t *testing.T) {
	r := roster(t)

	if got := len(r.Apply(Filter{})); got != 2 {
		t.Errorf("no filter: got %d members, want the 2 current ones", got)
	}
	if got := len(r.Apply(Filter{Show: "former"})); got != 1 {
		t.Errorf("former only: got %d, want 1", got)
	}
	if got := len(r.Apply(Filter{Show: "all"})); got != 3 {
		t.Errorf("everybody: got %d, want 3", got)
	}
	if got := len(r.Apply(Filter{Kind: config.KindVan})); got != 1 {
		t.Errorf("friends only: got %d, want 1", got)
	}
	if got := len(r.Apply(Filter{Fee: Overdue})); got != 1 {
		t.Errorf("overdue only: got %d, want 1 (Bo)", got)
	}
}

// Several words all have to match, which is what makes "anna 14" find Anna in
// apartment 1403 rather than every Anna in the house.
func TestSearchRequiresEveryWordToMatch(t *testing.T) {
	r := roster(t)
	tests := []struct {
		query string
		want  int
	}{
		{"anna", 1},
		{"anna 1403", 1},
		{"anna 0201", 0},
		{"ANNA", 1},
		{"example.test", 2},
		{"", 2},
	}
	for _, tc := range tests {
		if got := len(r.Apply(Filter{Query: tc.query})); got != tc.want {
			t.Errorf("search %q: got %d, want %d", tc.query, got, tc.want)
		}
	}
}

func TestCountsSummariseTheRegister(t *testing.T) {
	c := roster(t).Count()
	if c.Bo != 1 || c.Van != 1 {
		t.Errorf("kinds: got %d bo, %d van; want 1 and 1", c.Bo, c.Van)
	}
	if c.Current != 2 || c.Former != 1 {
		t.Errorf("standing: got %d current, %d former; want 2 and 1", c.Current, c.Former)
	}
	if c.Paid != 1 || c.Overdue != 1 {
		t.Errorf("fees: got %d paid, %d overdue; want 1 and 1", c.Paid, c.Overdue)
	}
	// Only Bo still owes, and only 200 kr of it. A former member owes nothing.
	if c.OwedKr != 200 {
		t.Errorf("outstanding: got %d kr, want 200", c.OwedKr)
	}
}

// Sorting by the fee has to put the problem at the top. Ordering by the word
// would file "Betald" before "Förfallen" and bury exactly what is being
// looked for.
func TestSortByFeePutsTheWorstFirst(t *testing.T) {
	rows := roster(t).Apply(Filter{Show: "all"})
	Sort(rows, "fee", false)
	want := []FeeState{Overdue, Paid, Ended}
	for i, w := range want {
		if rows[i].Fee != w {
			t.Errorf("row %d: got %q, want %q", i, rows[i].Fee, w)
		}
	}
}

func TestSortByApartmentPutsTheBlanksLast(t *testing.T) {
	rows := roster(t).Apply(Filter{})
	Sort(rows, "apartment", false)
	if rows[0].Member.Apartment == "" {
		t.Error("a member with no apartment sorted above one with an apartment")
	}
}

func TestAnUnknownSortKeyFallsBackToTheRegisterOrder(t *testing.T) {
	if got := ValidSort("../../etc/passwd"); got != "name" {
		t.Errorf("got %q, want %q", got, "name")
	}
	rows := roster(t).Apply(Filter{Show: "all"})
	Sort(rows, ValidSort("nonsense"), false)
	if rows[0].Member.LastName != "Andersson" {
		t.Errorf("got %q first, want Andersson", rows[0].Member.LastName)
	}
}

// Importing a member who joined a decade ago must not invent a decade of
// unpaid fees. The register can only vouch for what it was told about, and
// the day the row was created is where that starts.
func TestAnImportedMemberIsNotChasedForYearsBeforeTheRegisterExisted(t *testing.T) {
	cfg := testConfig()
	now := day(2026, time.June, 1)

	imported := member(day(2014, time.April, 1))
	imported.CreatedAt = now // written down today, as part of the migration

	st := Compute(imported, nil, cfg, now)
	if len(st.Unpaid) != 0 {
		t.Errorf("chased for %v; an imported member owes nothing for years the "+
			"register never saw", st.Unpaid)
	}
	if st.Fee != Due {
		t.Errorf("fee state: got %q, want %q — this year is still expected", st.Fee, Due)
	}
	// Twelve years a member all the same: that is the fact worth importing.
	if st.Years != 12 {
		t.Errorf("tenure: got %d years, want 12", st.Years)
	}
}

// An association that has kept proper books can ask for the whole history.
func TestChaseFromYearOverridesTheDefaultFloor(t *testing.T) {
	cfg, err := config.Parse([]byte(`
site: {title: Test, language: sv, timezone: Europe/Stockholm}
membership: {fee_kr: 200, due_on: "03-31", grace_days: 14, new_member_days: 45, chase_from_year: 2023}
groups:
  - {kind: bo, email: bo@example.test}
  - {kind: van, email: van@example.test}
`))
	if err != nil {
		t.Fatal(err)
	}
	m := member(day(2020, time.January, 1))
	st := Compute(m, paid(2026), cfg, day(2026, time.June, 1))

	want := []int{2023, 2024, 2025}
	if len(st.Unpaid) != len(want) {
		t.Fatalf("unpaid years: got %v, want %v", st.Unpaid, want)
	}
	for i, y := range want {
		if st.Unpaid[i] != y {
			t.Errorf("unpaid[%d]: got %d, want %d", i, st.Unpaid[i], y)
		}
	}
	if st.OwedKr != 600 {
		t.Errorf("owed: got %d kr, want 600", st.OwedKr)
	}
	if got := st.Owing(); got != 2023 {
		t.Errorf("oldest outstanding year: got %d, want 2023", got)
	}
}
