// Package demo fills an empty register with a plausible association, so that
// somebody trying the registry out sees a house with a history rather than an
// empty table and a "no members yet".
//
// Everybody in here is invented. Demo mode never reaches Google, so no
// address can be written to a real group and no card can appear in a real
// address book — the addresses are on example.test for good measure.
package demo

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

type person struct {
	first, last string
	kind        config.Kind
	apartment   string
	// joinedYearsAgo and joinedMonth place them in the association's history.
	joinedYearsAgo int
	joinedMonth    time.Month
	// unpaidYears are the years they did not pay, counted back from this one.
	// 0 means they have not paid for the current year, which is the case the
	// cashiers care about.
	unpaid []int
	phone  string
	note   string
}

// The cast. Roughly two thirds bomedlemmar, which is about the shape of the
// real association, with a handful of long-standing friends and a couple of
// people who joined last month and have not paid yet — the whole point of
// being able to write somebody down before the money lands.
var cast = []person{
	{"Anna", "Andersson", config.KindBo, "1403", 11, time.March, nil, "070-123 45 67", ""},
	{"Bo", "Bengtsson", config.KindBo, "0702", 9, time.September, nil, "070-234 56 78", ""},
	{"Cecilia", "Dahl", config.KindBo, "1201", 8, time.January, nil, "073-345 67 89", ""},
	{"David", "Ek", config.KindBo, "0304", 7, time.June, []int{0}, "070-456 78 90", ""},
	{"Elin", "Forsberg", config.KindBo, "0508", 6, time.February, nil, "076-567 89 01", "Sitter i styrelsen"},
	{"Farid", "Hassan", config.KindBo, "1105", 6, time.November, nil, "070-678 90 12", ""},
	{"Greta", "Lind", config.KindBo, "0601", 5, time.April, nil, "073-789 01 23", ""},
	{"Hugo", "Nyström", config.KindBo, "0907", 5, time.August, nil, "070-890 12 34", ""},
	{"Ingrid", "Öberg", config.KindBo, "1302", 4, time.January, nil, "076-901 23 45", ""},
	{"Jonas", "Pettersson", config.KindBo, "0405", 4, time.May, []int{0, 1}, "070-012 34 56", "Påmind i mars"},
	{"Karin", "Qvist", config.KindBo, "1006", 3, time.October, nil, "073-123 45 60", ""},
	{"Leif", "Rundkvist", config.KindBo, "0803", 3, time.March, nil, "070-234 56 71", ""},
	{"Maja", "Sandström", config.KindBo, "1104", 2, time.July, nil, "076-345 67 82", ""},
	{"Nils", "Tegnér", config.KindBo, "0206", 2, time.December, nil, "070-456 78 93", ""},
	{"Olga", "Ulfsdotter", config.KindBo, "1501", 1, time.February, nil, "073-567 89 04", ""},
	{"Peter", "Vikström", config.KindBo, "0709", 1, time.September, []int{0}, "070-678 90 15", ""},
	{"Rania", "Aziz", config.KindBo, "1207", 0, time.January, nil, "076-789 01 26", "Flyttade in i januari"},
	{"Sven", "Bergman", config.KindBo, "", 0, time.August, []int{0}, "070-890 12 37", "Väntar på lägenhet"},

	{"Tove", "Cederberg", config.KindVan, "", 12, time.May, nil, "073-901 23 48", "Med sedan starten"},
	{"Urban", "Dahlgren", config.KindVan, "", 9, time.March, nil, "070-012 34 59", ""},
	{"Vera", "Engström", config.KindVan, "", 7, time.October, nil, "076-123 45 61", ""},
	{"Wilhelm", "Fransson", config.KindVan, "", 6, time.April, []int{1}, "070-234 56 72", ""},
	{"Yasmin", "Gustafsson", config.KindVan, "", 5, time.June, nil, "073-345 67 83", ""},
	{"Zara", "Holm", config.KindVan, "", 4, time.November, nil, "070-456 78 94", ""},
	{"Åke", "Isaksson", config.KindVan, "", 3, time.February, nil, "076-567 89 05", ""},
	{"Ärling", "Jonsson", config.KindVan, "", 2, time.August, []int{0}, "070-678 90 16", ""},
	{"Örjan", "Karlsson", config.KindVan, "", 1, time.January, nil, "073-789 01 27", ""},
	{"Beatrice", "Lundqvist", config.KindVan, "", 0, time.March, nil, "070-890 12 38", "Hörde talas om huset på Öppet hus"},
	{"Caspar", "Möller", config.KindVan, "", 0, time.July, []int{0}, "076-901 23 49", "Anmälde intresse på studiebesöket"},
}

// former members keep their row, which is how the association can say how
// many people have passed through it.
var former = []person{
	{"Dagny", "Nordin", config.KindBo, "0801", 14, time.April, nil, "", "Flyttade till Göteborg"},
	{"Erik", "Palmgren", config.KindVan, "", 10, time.September, nil, "", ""},
}

// Seed writes the example association. It is a no-op when the register
// already holds anything, so restarting a demo does not pile a second cast on
// top of the first.
func Seed(ctx context.Context, st *store.Store, cfg *config.Config, now time.Time) (int, error) {
	existing, err := st.CountMembers(ctx)
	if err != nil {
		return 0, fmt.Errorf("check for existing members: %w", err)
	}
	if existing > 0 {
		return 0, nil
	}

	// A fixed seed keeps the demo identical every time, which makes it a
	// stable thing to point at in a README or a bug report.
	rng := rand.New(rand.NewSource(20260905))
	loc := cfg.Location()
	now = now.In(loc)
	created := 0

	for _, p := range cast {
		m := build(p, cfg, now, loc, rng)
		if err := st.CreateMember(ctx, m); err != nil {
			return created, fmt.Errorf("seed %s: %w", m.Name(), err)
		}
		created++
		if err := pay(ctx, st, cfg, m, p, now, loc, rng); err != nil {
			return created, err
		}
	}

	for _, p := range former {
		m := build(p, cfg, now, loc, rng)
		left := m.JoinedOn.AddDate(p.joinedYearsAgo-1, 4, 0)
		m.LeftOn = sql.NullTime{Time: left, Valid: true}
		m.Note = p.note
		if err := st.CreateMember(ctx, m); err != nil {
			return created, fmt.Errorf("seed %s: %w", m.Name(), err)
		}
		created++
		// A former member paid right up until they left, which is what makes
		// the tenure column interesting.
		for y := m.JoinedOn.Year(); y <= left.Year(); y++ {
			if err := st.RecordPayment(ctx, store.Payment{
				MemberID: m.ID, Year: y, AmountKr: cfg.Membership.FeeFor(m.Kind),
				PaidOn:       time.Date(y, time.February, 12, 0, 0, 0, 0, loc),
				Method:       "Bankgiro",
				RegisteredBy: "ekonomi@rudbeckia.nu",
				RegisteredAt: now.AddDate(0, 0, -30),
			}); err != nil {
				return created, err
			}
		}
	}

	if err := seedProposals(ctx, st, cfg, now, loc); err != nil {
		return created, err
	}
	return created, nil
}

func build(p person, cfg *config.Config, now time.Time, loc *time.Location, rng *rand.Rand) store.Member {
	joined := time.Date(now.Year()-p.joinedYearsAgo, p.joinedMonth, 1+rng.Intn(27), 0, 0, 0, 0, loc)
	// Somebody who "joined this year in July" cannot have joined next July.
	if joined.After(now) {
		joined = now.AddDate(0, 0, -1-rng.Intn(20))
	}
	created := joined
	if created.Before(now.AddDate(-3, 0, 0)) {
		// The register itself is newer than the association, so old members
		// were written down when it was set up rather than when they joined.
		created = now.AddDate(-3, 0, 0)
	}
	return store.Member{
		// Folded to ASCII, because the id goes in a URL: "demo-caspar-möller"
		// works but reads badly and is easy to mistype into a lookup.
		ID:        fmt.Sprintf("demo-%s-%s", ascii(p.first), ascii(p.last)),
		FirstName: p.first,
		LastName:  p.last,
		Email:     fmt.Sprintf("%s.%s@example.test", ascii(p.first), ascii(p.last)),
		Phone:     p.phone,
		Kind:      p.kind,
		Apartment: p.apartment,
		JoinedOn:  joined,
		Note:      p.note,
		CreatedAt: created,
		CreatedBy: "ny@rudbeckia.nu",
		UpdatedAt: created,
		UpdatedBy: "ny@rudbeckia.nu",
	}
}

// pay records every year's fee except the ones the cast member skipped.
func pay(ctx context.Context, st *store.Store, cfg *config.Config, m store.Member,
	p person, now time.Time, loc *time.Location, rng *rand.Rand) error {

	skip := map[int]bool{}
	for _, back := range p.unpaid {
		skip[now.Year()-back] = true
	}
	for y := m.JoinedOn.Year(); y <= now.Year(); y++ {
		if skip[y] {
			continue
		}
		// The fee lands somewhere in the first months of the year, or shortly
		// after joining for the year somebody joined.
		when := time.Date(y, time.January, 10+rng.Intn(70), 0, 0, 0, 0, loc)
		if y == m.JoinedOn.Year() && when.Before(m.JoinedOn) {
			when = m.JoinedOn.AddDate(0, 0, 3+rng.Intn(14))
		}
		if when.After(now) {
			continue
		}
		if err := st.RecordPayment(ctx, store.Payment{
			MemberID: m.ID, Year: y, AmountKr: cfg.Membership.FeeFor(m.Kind),
			PaidOn: when, Method: "Bankgiro",
			RegisteredBy: "ekonomi@rudbeckia.nu", RegisteredAt: when.AddDate(0, 0, 2),
		}); err != nil {
			return fmt.Errorf("seed a payment for %s: %w", m.Name(), err)
		}
	}
	return nil
}

// seedProposals leaves a couple of changes waiting for the board, so the
// approval queue is not an empty page nobody understands the point of.
func seedProposals(ctx context.Context, st *store.Store, cfg *config.Config,
	now time.Time, loc *time.Location) error {

	// The ids are the ones build() derives, so a change to the cast that
	// breaks them has to be a loud failure rather than a demo that quietly
	// comes up with an empty approval queue and nobody the wiser.
	rania, err := st.Member(ctx, "demo-rania-aziz", loc)
	if err != nil {
		return fmt.Errorf("seed a proposal: the cast has no demo-rania-aziz: %w", err)
	}
	before := store.SnapshotOf(rania)
	after := before
	after.Phone = "070-555 12 34"
	after.Apartment = "1208"
	if err := st.CreateProposal(ctx, store.Proposal{
		ID: "demo-proposal-1", MemberID: rania.ID, Kind: store.ProposeUpdate,
		Before: before, After: after,
		Reason:     "Rania har bytt lägenhet inom huset och fått nytt nummer.",
		Status:     store.Pending,
		ProposedBy: "ny@rudbeckia.nu", ProposedAt: now.AddDate(0, 0, -2),
	}); err != nil {
		return err
	}

	caspar, err := st.Member(ctx, "demo-caspar-moller", loc)
	if err != nil {
		return fmt.Errorf("seed a proposal: the cast has no demo-caspar-moller: %w", err)
	}
	snapshot := store.SnapshotOf(caspar)
	return st.CreateProposal(ctx, store.Proposal{
		ID: "demo-proposal-2", MemberID: caspar.ID, Kind: store.ProposeDelete,
		Before: snapshot, After: snapshot,
		Reason:     "Caspar hörde av sig och vill inte gå med trots allt.",
		Status:     store.Pending,
		ProposedBy: "ny@rudbeckia.nu", ProposedAt: now.AddDate(0, 0, -1),
	})
}

// ascii folds the Swedish letters, so a demo address is one a mail server
// would actually have accepted.
func ascii(s string) string {
	replacements := map[rune]string{
		'å': "a", 'ä': "a", 'ö': "o", 'é': "e", 'Å': "a", 'Ä': "a", 'Ö': "o",
	}
	out := make([]rune, 0, len(s))
	for _, r := range lower(s) {
		if sub, ok := replacements[r]; ok {
			out = append(out, []rune(sub)...)
			continue
		}
		out = append(out, r)
	}
	return string(out)
}

func lower(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		switch r {
		case 'Å':
			r = 'å'
		case 'Ä':
			r = 'ä'
		case 'Ö':
			r = 'ö'
		}
		out = append(out, r)
	}
	return string(out)
}
