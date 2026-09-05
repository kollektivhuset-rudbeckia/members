package web

import (
	"encoding/csv"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
)

// tableQuery is everything the register page reads out of its query string:
// what to show, and in what order. It round-trips through the URL, so a
// filtered, sorted view is a link somebody can send to the rest of the board.
type tableQuery struct {
	Filter membership.Filter
	Sort   string
	Desc   bool
	// Page is 1-based. PerPage of 0 means everything on one page.
	Page    int
	PerPage int
}

// perPageChoices are the page sizes on offer. 0 is "all", which stays
// available because a register of a few hundred is small enough to want whole
// sometimes — to print it, or to search the lot with the browser's own find.
var perPageChoices = []int{10, 25, 50, 100, 0}

// defaultPerPage keeps the first screen short. Ten is a screenful without
// scrolling on most machines, and the filter above the table is a better way
// to find one person than a hundred rows are.
const defaultPerPage = 10

func readTableQuery(r *http.Request) tableQuery {
	q := r.URL.Query()
	kind, _ := config.ParseKind(q.Get("typ"))
	show := q.Get("visa")
	if show != "former" && show != "all" {
		show = "current"
	}
	perPage := defaultPerPage
	if raw := q.Get("antal"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && validPerPage(n) {
			perPage = n
		}
	}
	page := 1
	if n, err := strconv.Atoi(q.Get("sida")); err == nil && n > 1 {
		page = n
	}
	return tableQuery{
		Filter: membership.Filter{
			Kind:  kind,
			Fee:   readFeeState(q.Get("avgift")),
			Show:  show,
			Query: strings.TrimSpace(q.Get("sok")),
		},
		Sort:    membership.ValidSort(q.Get("ordna")),
		Desc:    q.Get("fallande") == "1",
		Page:    page,
		PerPage: perPage,
	}
}

func validPerPage(n int) bool {
	for _, c := range perPageChoices {
		if c == n {
			return true
		}
	}
	return false
}

// Pages is one screenful of a sorted register, plus everything a pager needs
// to describe itself.
type Pages struct {
	Rows []membership.Status
	// Page is the one being shown, Count how many there are.
	Page    int
	Count   int
	PerPage int
	// First and Last are the 1-based positions of the rows on this page, for
	// "51–100 av 114".
	First, Last int
	Total       int
	// Complete reports whether every matching row is on this page. It is what
	// decides whether the search box may filter in the browser: filtering
	// only the visible page while quietly hiding the rest would be worse than
	// not filtering at all.
	Complete bool
	Numbers  []int
}

// HasPrev and HasNext keep the arithmetic out of the template.
func (p Pages) HasPrev() bool { return p.Page > 1 }
func (p Pages) HasNext() bool { return p.Page < p.Count }
func (p Pages) Prev() int     { return p.Page - 1 }
func (p Pages) Next() int     { return p.Page + 1 }

// paginate cuts the sorted rows down to the page asked for.
func paginate(rows []membership.Status, page, perPage int) Pages {
	total := len(rows)
	if perPage <= 0 || total == 0 {
		return Pages{Rows: rows, Page: 1, Count: 1, PerPage: 0,
			First: min(1, total), Last: total, Total: total, Complete: true}
	}
	count := (total + perPage - 1) / perPage
	if page > count {
		page = count
	}
	if page < 1 {
		page = 1
	}
	start := (page - 1) * perPage
	end := start + perPage
	if end > total {
		end = total
	}
	numbers := make([]int, 0, count)
	for i := 1; i <= count; i++ {
		numbers = append(numbers, i)
	}
	return Pages{
		Rows: rows[start:end], Page: page, Count: count, PerPage: perPage,
		First: start + 1, Last: end, Total: total,
		Complete: count == 1, Numbers: numbers,
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func readFeeState(s string) membership.FeeState {
	switch membership.FeeState(s) {
	case membership.Paid:
		return membership.Paid
	case membership.Due:
		return membership.Due
	case membership.Overdue:
		return membership.Overdue
	case membership.Ended:
		return membership.Ended
	}
	return ""
}

// Values rebuilds the query string, which is how the sortable column headings
// and the filter form keep each other's settings instead of resetting them.
func (t tableQuery) Values() url.Values {
	v := url.Values{}
	if t.Filter.Kind != "" {
		v.Set("typ", string(t.Filter.Kind))
	}
	if t.Filter.Fee != "" {
		v.Set("avgift", string(t.Filter.Fee))
	}
	if t.Filter.Show != "" && t.Filter.Show != "current" {
		v.Set("visa", t.Filter.Show)
	}
	if t.Filter.Query != "" {
		v.Set("sok", t.Filter.Query)
	}
	if t.Sort != "" && t.Sort != "name" {
		v.Set("ordna", t.Sort)
	}
	if t.Desc {
		v.Set("fallande", "1")
	}
	if t.PerPage != defaultPerPage {
		v.Set("antal", strconv.Itoa(t.PerPage))
	}
	return v
}

// PageLink is one page of the current view. The page number is the only thing
// that changes; every filter and the sort come along.
func (t tableQuery) PageLink(base string, page int) string {
	v := t.Values()
	if page > 1 {
		v.Set("sida", strconv.Itoa(page))
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

// PerPageLink switches the page size and returns to the first page, because
// "page 3 of 5" means something different once the pages are twice the size.
func (t tableQuery) PerPageLink(base string, n int) string {
	v := t.Values()
	v.Del("antal")
	v.Del("sida")
	if n != defaultPerPage {
		v.Set("antal", strconv.Itoa(n))
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

// PerPageChoices is the sizes on offer, for the template.
func (t tableQuery) PerPageChoices() []int { return perPageChoices }

// DefaultPerPage lets a template tell an unusual page size from the ordinary
// one without knowing the number itself.
func (t tableQuery) DefaultPerPage() int { return defaultPerPage }

// Smallest is the shortest page on offer, which is the point below which
// there is nothing worth paging.
func (t tableQuery) Smallest() int { return perPageChoices[0] }

// SortLink is the address a column heading points at: the same view ordered
// by that column, flipping the direction if it is already the one in use.
func (t tableQuery) SortLink(base, key string) string {
	v := t.Values()
	v.Del("sida")
	v.Set("ordna", key)
	if t.Sort == key && !t.Desc {
		v.Set("fallande", "1")
	} else {
		v.Del("fallande")
	}
	if len(v) == 0 {
		return base
	}
	return base + "?" + v.Encode()
}

// SortState is "ascending", "descending" or "" for a column, which is exactly
// what aria-sort wants.
func (t tableQuery) SortState(key string) string {
	if t.Sort != key {
		return ""
	}
	if t.Desc {
		return "descending"
	}
	return "ascending"
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request, v *view) {
	roster, err := s.roster(r.Context())
	if err != nil {
		s.log.Error("could not read the register", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}
	q := readTableQuery(r)
	rows := roster.Apply(q.Filter)
	membership.Sort(rows, q.Sort, q.Desc)
	pages := paginate(rows, q.Page, q.PerPage)

	v.Title = i18n.T(v.Lang, "register.title")
	v.Data = map[string]any{
		"Rows":      pages.Rows,
		"Pages":     pages,
		"Counts":    roster.Count(),
		"Query":     q,
		"Total":     len(roster.All),
		"SheetURL":  s.sheetURL(),
		"ExportURL": exportURL(r),
		"Kinds":     s.kindOptions(v.Lang),
		"Empty":     len(roster.All) == 0,
	}
	s.render(w, r, http.StatusOK, "index.html", v)
}

// kindOption pairs a membership with the words for it, so the filter, the
// form and the table all name the two kinds the same way.
type kindOption struct {
	Kind  config.Kind
	Label string
	Group string
}

// kindKey and feeKey build a catalogue key from a domain value. They are
// functions rather than inline concatenation so that the test which scans for
// missing phrases does not see half a key and report "kind." as missing — the
// whole families are checked by TestEveryComputedPhraseExists instead.
func kindKey(k config.Kind) string { return "kind." + string(k) }

func feeKey(f membership.FeeState) string { return "fee." + string(f) }

func (s *Server) kindOptions(lang i18n.Lang) []kindOption {
	out := make([]kindOption, 0, len(config.Kinds))
	for _, k := range config.Kinds {
		o := kindOption{Kind: k, Label: i18n.T(lang, kindKey(k))}
		if g, ok := s.cfg.GroupFor(k); ok {
			o.Group = g.Email
		}
		out = append(out, o)
	}
	return out
}

func exportURL(r *http.Request) string {
	if r.URL.RawQuery == "" {
		return "/export.csv"
	}
	return "/export.csv?" + r.URL.RawQuery
}

// handleExport writes the current view as a spreadsheet file. It honours the
// filter, so "export the friends who have not paid" is one click from having
// looked at exactly that.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request, v *view) {
	roster, err := s.roster(r.Context())
	if err != nil {
		http.Error(w, "could not read the register", http.StatusInternalServerError)
		return
	}
	q := readTableQuery(r)
	rows := roster.Apply(q.Filter)
	membership.Sort(rows, q.Sort, q.Desc)

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		"attachment; filename=\"medlemmar-"+v.Now.Format("2006-01-02")+".csv\"")
	// A byte-order mark makes Excel open the UTF-8 file with the right
	// encoding instead of mangling every å.
	w.Write([]byte{0xEF, 0xBB, 0xBF})

	cw := csv.NewWriter(w)
	// Semicolons: a Swedish Excel reads a comma as a decimal point and puts
	// the whole row in one cell.
	cw.Comma = ';'
	defer cw.Flush()

	lang := v.Lang
	cw.Write([]string{
		i18n.T(lang, "csv.name"), i18n.T(lang, "csv.firstname"), i18n.T(lang, "csv.lastname"),
		i18n.T(lang, "csv.email"), i18n.T(lang, "csv.phone"), i18n.T(lang, "csv.kind"),
		i18n.T(lang, "csv.alsoin"),
		i18n.T(lang, "csv.apartment"), i18n.T(lang, "csv.joined"), i18n.T(lang, "csv.tenure"),
		i18n.T(lang, "csv.status"), i18n.T(lang, "csv.fee"), i18n.T(lang, "csv.paid"),
		i18n.T(lang, "csv.paidon"), i18n.T(lang, "csv.paidyears"), i18n.T(lang, "csv.note"),
	})
	for _, row := range rows {
		m := row.Member
		years := make([]string, 0, len(row.PaidYears))
		for _, y := range row.PaidYears {
			years = append(years, strconv.Itoa(y))
		}
		paidOn := ""
		if row.HasPaid {
			paidOn = i18n.ISODate(row.Payment.PaidOn.In(v.Loc))
		}
		left := ""
		if !m.Current() {
			left = i18n.ISODate(m.LeftOn.Time.In(v.Loc))
		}
		cw.Write([]string{
			m.Name(), m.FirstName, m.LastName, m.Email, m.Phone,
			i18n.T(lang, kindKey(m.Kind)),
			strings.Join(kindStrings(m.AlsoIn), " "),
			m.Apartment,
			i18n.ISODate(m.JoinedOn.In(v.Loc)),
			strconv.Itoa(row.Years),
			statusWord(lang, row, left),
			strconv.Itoa(row.OwedKr), strconv.Itoa(row.PaidKr), paidOn,
			strings.Join(years, " "), m.Note,
		})
	}
}

// statusWord is the one-word standing for the export.
func statusWord(lang i18n.Lang, s membership.Status, left string) string {
	if !s.Current() {
		return i18n.T(lang, feeKey(membership.Ended)) + " " + left
	}
	return i18n.T(lang, feeKey(s.Fee))
}
