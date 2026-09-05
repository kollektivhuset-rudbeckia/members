package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

// handlePayments is the cashiers' page: this year's fees, who still owes, and
// a running total against the bank statement they have open beside it.
func (s *Server) handlePayments(w http.ResponseWriter, r *http.Request, v *view) {
	roster, err := s.roster(r.Context())
	if err != nil {
		s.log.Error("could not read the register", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}

	year := v.Now.Year()
	if raw := r.URL.Query().Get("ar"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n >= 1900 && n <= v.Now.Year()+1 {
			year = n
		}
	}

	q := readTableQuery(r)
	rows := roster.Apply(q.Filter)
	// The page exists to chase money, so it opens with the worst first rather
	// than alphabetically. Clicking a column heading takes over from there.
	if r.URL.Query().Get("ordna") == "" {
		membership.Sort(rows, "fee", false)
	} else {
		membership.Sort(rows, q.Sort, q.Desc)
	}

	counts := roster.Count()
	var received int
	for _, s := range roster.All {
		if kr, ok := s.PaidByYear[year]; ok {
			received += kr
		}
	}

	v.Title = i18n.T(v.Lang, "fees.title")
	v.Data = map[string]any{
		"Rows":      rows,
		"Counts":    counts,
		"Query":     q,
		"Year":      year,
		"Years":     yearsInPlay(roster, v.Now.Year()),
		"Received":  received,
		"Overdue":   roster.Overdue(),
		"ExportURL": exportURL(r),
		"Kinds":     s.kindOptions(v.Lang),
		"Fee":       s.cfg.Membership,
	}
	s.render(w, r, http.StatusOK, "payments.html", v)
}

// yearsInPlay is the years the cashier can look at: back to the earliest fee
// anybody has ever paid, and never past this one.
func yearsInPlay(r membership.Roster, thisYear int) []int {
	earliest := thisYear
	for _, s := range r.All {
		for _, y := range s.PaidYears {
			if y < earliest {
				earliest = y
			}
		}
		if y := s.Member.JoinedOn.Year(); y < earliest {
			earliest = y
		}
	}
	out := make([]int, 0, thisYear-earliest+1)
	for y := thisYear; y >= earliest; y-- {
		out = append(out, y)
	}
	return out
}

// handlePay records a fee. Only the cashier gets here — they are the one
// looking at the bank statement, and a payment that somebody else ticked off
// would be a number nobody can vouch for.
func (s *Server) handlePay(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	m, ok := s.member(w, r, v)
	if !ok {
		return
	}

	year, err := strconv.Atoi(strings.TrimSpace(r.FormValue("ar")))
	if err != nil || year < 1900 || year > v.Now.Year()+1 {
		s.flash(w, "error", "flash.badyear")
		http.Redirect(w, r, s.backTo(r, "/medlem/"+m.ID), http.StatusSeeOther)
		return
	}

	amount := s.cfg.Membership.FeeFor(m.Kind)
	if raw := strings.TrimSpace(r.FormValue("belopp")); raw != "" {
		// A comma is how a Swedish keyboard writes a decimal point, and the
		// register counts in whole kronor anyway — so take the krona part and
		// do not argue about the öre.
		raw = strings.ReplaceAll(raw, " ", "")
		if i := strings.IndexAny(raw, ",."); i >= 0 {
			raw = raw[:i]
		}
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			s.flash(w, "error", "flash.badamount")
			http.Redirect(w, r, s.backTo(r, "/medlem/"+m.ID), http.StatusSeeOther)
			return
		}
		amount = n
	}

	paidOn := v.Now
	if raw := strings.TrimSpace(r.FormValue("datum")); raw != "" {
		t, err := store.ParseDay(raw, v.Loc)
		if err != nil {
			s.flash(w, "error", "flash.baddate")
			http.Redirect(w, r, s.backTo(r, "/medlem/"+m.ID), http.StatusSeeOther)
			return
		}
		paidOn = t
	}

	p := store.Payment{
		MemberID: m.ID, Year: year, AmountKr: amount, PaidOn: paidOn,
		Method:       strings.TrimSpace(r.FormValue("satt")),
		Note:         strings.TrimSpace(r.FormValue("kommentar")),
		RegisteredBy: v.Session.Email, RegisteredAt: s.now(),
	}
	if err := s.store.RecordPayment(r.Context(), p); err != nil {
		s.log.Error("could not record a payment", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}

	s.audit(r.Context(), v, "payment.recorded", m,
		strconv.Itoa(year)+": "+i18n.Money(amount)+" "+i18n.ISODate(paidOn.In(v.Loc)))
	// The spreadsheet the cashiers calculate in is a sync target, so a
	// payment has to reach it without waiting for the next tick.
	s.sync.Nudge(sync.TriggerPayment)
	s.flash(w, "ok", "flash.paid", m.Name(), strconv.Itoa(year))
	s.log.Info("payment recorded", "member", m.ID, "year", year, "kr", amount, "by", v.Session.Email)
	http.Redirect(w, r, s.backTo(r, "/medlem/"+m.ID), http.StatusSeeOther)
}

// handleUnpay takes a fee back off the record, for the ordinary case of a
// cashier ticking the wrong row.
func (s *Server) handleUnpay(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	m, ok := s.member(w, r, v)
	if !ok {
		return
	}
	year, err := strconv.Atoi(strings.TrimSpace(r.FormValue("ar")))
	if err != nil {
		s.flash(w, "error", "flash.badyear")
		http.Redirect(w, r, s.backTo(r, "/medlem/"+m.ID), http.StatusSeeOther)
		return
	}
	if err := s.store.WithdrawPayment(r.Context(), m.ID, year); err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("could not withdraw a payment", "err", err)
			s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
			return
		}
	}
	s.audit(r.Context(), v, "payment.withdrawn", m, strconv.Itoa(year))
	s.sync.Nudge(sync.TriggerPayment)
	s.flash(w, "warn", "flash.unpaid", m.Name(), strconv.Itoa(year))
	s.log.Info("payment withdrawn", "member", m.ID, "year", year, "by", v.Session.Email)
	http.Redirect(w, r, s.backTo(r, "/medlem/"+m.ID), http.StatusSeeOther)
}

// backTo returns to the page the form was submitted from, so ticking off ten
// fees on the payments page does not bounce through ten member pages.
func (s *Server) backTo(r *http.Request, fallback string) string {
	if back := r.FormValue("tillbaka"); back != "" {
		return safeNext(back)
	}
	return fallback
}
