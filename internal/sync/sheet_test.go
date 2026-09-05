package sync

import (
	"testing"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Parse([]byte(`
site: {title: Test, language: sv, timezone: Europe/Stockholm}
membership: {fee_kr: 200, due_on: "03-31", grace_days: 14, new_member_days: 45}
groups:
  - {kind: bo, email: bo@example.test}
  - {kind: van, email: van@example.test}
sheet: {id: abc, tab: Medlemmar, years: 3}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTheSheetIsAHeaderTheMembersAndAFooter(t *testing.T) {
	cfg := testConfig(t)
	loc := cfg.Location()
	now := time.Date(2026, time.June, 1, 0, 0, 0, 0, loc)
	joined := time.Date(2022, time.March, 1, 0, 0, 0, 0, loc)

	anna := store.Member{ID: "a", FirstName: "Anna", LastName: "Andersson",
		Email: "anna@example.test", Phone: "070-1", Kind: config.KindBo,
		Apartment: "1403", JoinedOn: joined, CreatedAt: joined, Note: "styrelsen"}
	payments := map[string][]store.Payment{"a": {
		{Year: 2026, AmountKr: 200, PaidOn: time.Date(2026, time.February, 3, 0, 0, 0, 0, loc)},
		{Year: 2025, AmountKr: 150, PaidOn: time.Date(2025, time.February, 3, 0, 0, 0, 0, loc)},
	}}

	grid := Grid(membership.Build([]store.Member{anna}, payments, cfg, now), cfg, now)

	if len(grid) != 4 { // header, Anna, a blank line, the note
		t.Fatalf("got %d rows, want 4:\n%v", len(grid), grid)
	}
	header, row := grid[0], grid[1]
	if len(header) != len(row) {
		t.Errorf("the header has %d columns and the member row %d", len(header), len(row))
	}

	// Three year columns were asked for, and the sheet exists so the cashiers
	// can add them up — so they hold bare numbers, not ticks or "200 kr".
	index := map[string]int{}
	for i, cell := range header {
		index[cell.(string)] = i
	}
	for _, year := range []string{"2026", "2025", "2024"} {
		if _, ok := index[year]; !ok {
			t.Errorf("no column for %s: %v", year, header)
		}
	}
	if got := row[index["2026"]]; got != 200 {
		t.Errorf("2026 column: got %v (%T), want the number 200", got, got)
	}
	if got := row[index["2025"]]; got != 150 {
		t.Errorf("2025 column: got %v (%T), want the number 150", got, got)
	}
	// A year with no payment is blank, not a nought: a nought means the fee
	// was waived, and the two are different things to a cashier.
	if got := row[index["2024"]]; got != "" {
		t.Errorf("2024 column: got %v, want an empty cell", got)
	}
	if got := row[index["År som medlem"]]; got != 4 {
		t.Errorf("tenure column: got %v, want 4", got)
	}
	if got := row[index["Medlem sedan"]]; got != "2022-03-01" {
		t.Errorf("joined column: got %v, want an ISO date", got)
	}

	// The footer tells a cashier opening the tab in June whether they are
	// looking at something fresh.
	footer := grid[3]
	if len(footer) == 0 || footer[1] != "2026-06-01 00:00" {
		t.Errorf("footer: got %v", footer)
	}
}

func TestTheSheetNeverShowsMoreYearsThanTheRegisterHas(t *testing.T) {
	cfg := testConfig(t)
	loc := cfg.Location()
	now := time.Date(2026, time.June, 1, 0, 0, 0, 0, loc)
	joined := time.Date(2025, time.June, 1, 0, 0, 0, 0, loc)

	m := store.Member{ID: "a", FirstName: "Ny", LastName: "Medlem",
		Email: "ny@example.test", Kind: config.KindVan, JoinedOn: joined, CreatedAt: joined}
	grid := Grid(membership.Build([]store.Member{m}, nil, cfg, now), cfg, now)

	header := grid[0]
	for _, cell := range header {
		if cell == "2024" {
			t.Error("a column for a year before the association's first member")
		}
	}
}

func TestAFormerMemberIsInTheSheetWithoutAFee(t *testing.T) {
	cfg := testConfig(t)
	loc := cfg.Location()
	now := time.Date(2026, time.June, 1, 0, 0, 0, 0, loc)
	joined := time.Date(2020, time.January, 1, 0, 0, 0, 0, loc)

	gone := store.Member{ID: "g", FirstName: "Dagny", LastName: "Nordin",
		Email: "dagny@example.test", Kind: config.KindBo, JoinedOn: joined, CreatedAt: joined}
	gone.LeftOn.Time, gone.LeftOn.Valid = time.Date(2024, time.June, 1, 0, 0, 0, 0, loc), true

	grid := Grid(membership.Build([]store.Member{gone}, nil, cfg, now), cfg, now)
	header, row := grid[0], grid[1]
	index := map[string]int{}
	for i, cell := range header {
		index[cell.(string)] = i
	}
	if got := row[index["Status"]]; got != "Utträdd" {
		t.Errorf("status: got %v, want Utträdd", got)
	}
	if got := row[index["Avgift i år"]]; got != "" {
		t.Errorf("a former member should owe nothing: got %v", got)
	}
}

// The contact card is compared before it is written, so an unchanged register
// makes no calls at all. That is what lets the sync run every ten minutes for
// years without anybody noticing it.
func TestAnUnchangedContactIsNotRewritten(t *testing.T) {
	s := &Syncer{cfg: testConfig(t)}
	joined := time.Date(2022, time.March, 1, 0, 0, 0, 0, s.cfg.Location())
	m := store.Member{FirstName: "Anna", LastName: "Andersson",
		Email: "anna@example.test", Phone: "070-1", Kind: config.KindBo,
		Apartment: "1403", JoinedOn: joined}

	want := s.card(m)
	// What Google would hand back for a card the register wrote last time.
	have := want
	have.ResourceName, have.ETag = "people/c1", "etag1"

	if changed(have, want) {
		t.Error("an unchanged contact was reported as needing a write")
	}

	m.Phone = "070-2"
	if !changed(have, s.card(m)) {
		t.Error("a changed telephone number was not noticed")
	}

	m.Phone = "070-1"
	m.Apartment = "0101"
	if !changed(have, s.card(m)) {
		t.Error("a changed apartment was not noticed; it is written into the note")
	}
}

func TestTheContactNoteSaysWhatSortOfMemberAndSinceWhen(t *testing.T) {
	s := &Syncer{cfg: testConfig(t)}
	joined := time.Date(2022, time.March, 1, 0, 0, 0, 0, s.cfg.Location())

	bo := store.Member{FirstName: "A", LastName: "B", Kind: config.KindBo,
		Apartment: "1403", JoinedOn: joined}
	if got := s.note(bo); got != "Bomedlem sedan 2022-03-01 lgh 1403 · ur medlemsregistret" {
		t.Errorf("resident note: %q", got)
	}
	van := store.Member{FirstName: "A", LastName: "B", Kind: config.KindVan, JoinedOn: joined}
	if got := s.note(van); got != "Vänmedlem sedan 2022-03-01 · ur medlemsregistret" {
		t.Errorf("friend note: %q", got)
	}
}

// Every trigger renders as a translated phrase on the sync page, so the set
// has to stay closed and the catalogue has to keep up with it.
func TestTriggersAreAllAccountedFor(t *testing.T) {
	seen := map[Trigger]bool{}
	for _, tr := range Triggers {
		if seen[tr] {
			t.Errorf("%q appears twice", tr)
		}
		seen[tr] = true
		if string(tr) == "" {
			t.Error("a trigger has no name")
		}
	}
	for _, tr := range []Trigger{TriggerStart, TriggerSchedule, TriggerManual,
		TriggerMember, TriggerPayment, TriggerProposal} {
		if !seen[tr] {
			t.Errorf("%q is not in Triggers, so nothing checks it has a phrase", tr)
		}
	}
}

// A syncer with no Google client is what an unconfigured deployment is. It
// has to be inert rather than a crash.
func TestASyncerWithoutGoogleIsInert(t *testing.T) {
	var s *Syncer
	if s.Enabled() {
		t.Error("a nil syncer claims to be enabled")
	}
	s = New(testConfig(t), config.Runtime{}, nil, nil, nil)
	if s.Enabled() {
		t.Error("a syncer with no client claims to be enabled")
	}
	// Neither of these may panic or block.
	s.Nudge(TriggerMember)
	report := s.Once(nil, TriggerManual)
	if report.OK() {
		t.Error("a run with nothing to sync against reported success")
	}
	if report.Err == nil {
		t.Error("the report should say why nothing happened")
	}
}
