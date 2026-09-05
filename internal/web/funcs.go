package web

import (
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// funcs builds the template helpers for one language. Every helper that says
// anything in words closes over the language, so the templates never pass one
// and cannot pass the wrong one.
func (s *Server) funcs(lang i18n.Lang) template.FuncMap {
	l := string(lang)
	return template.FuncMap{
		"t":      func(key string, args ...any) string { return i18n.T(lang, key, args...) },
		"count":  func(unit string, n int) string { return i18n.Count(lang, unit, n) },
		"plural": func(unit string, n int) string { return i18n.Plural(lang, unit, n) },

		"dateLong":  func(t time.Time) string { return i18n.DateLong(lang, t) },
		"dateShort": func(t time.Time) string { return i18n.DateShort(lang, t) },
		"dayMonth":  func(t time.Time) string { return i18n.DayMonth(lang, t) },
		"monthYear": func(t time.Time) string { return i18n.MonthYear(lang, t) },
		"stamp":     func(t time.Time) string { return i18n.Stamp(lang, t) },
		"clock":     i18n.Clock,
		"isoDate":   i18n.ISODate,
		"titleCase": i18n.TitleCase,
		"tenure":    func(years, months int) string { return i18n.Tenure(lang, years, months) },
		"since":     func(from, now time.Time) string { return i18n.Since(lang, from, now) },
		"money":     i18n.Money,

		// duration renders the sync interval the way a person says it: "10
		// minuter", not "10m0s".
		"duration": func(d time.Duration) string {
			if d >= time.Hour && d%time.Hour == 0 {
				return i18n.Count(lang, "hour", int(d.Hours()))
			}
			return i18n.Count(lang, "minute", int(d.Minutes()))
		},

		// local moves a stored instant into the association's own timezone.
		// Every date on every page goes through it, so a server running in
		// UTC cannot show yesterday's date to somebody in Uppsala.
		"local": func(t time.Time) time.Time { return t.In(s.cfg.Location()) },

		// fee is the yearly fee for a kind of membership, so the amount on a
		// tick-off form comes from the configuration and not from a template.
		"fee": func(k any) int { return s.cfg.Membership.FeeFor(asKind(k)) },

		// account is the address behind a role, for the sentences that have
		// to say "ask ekonomi@rudbeckia.nu" rather than "ask the cashier".
		"account": func(role string) string { return s.rt.AccountFor(config.Role(role)) },

		// payment finds one year in a member's fees, or nothing.
		"payment": func(list []store.Payment, year int) *store.Payment {
			for i := range list {
				if list[i].Year == year {
					return &list[i]
				}
			}
			return nil
		},

		// paidIn is what somebody paid in a year, or -1 if nothing was
		// recorded. A payment of zero kronor is a real thing — a fee can be
		// waived — so a blank and a nought have to be different answers.
		"paidIn": func(st membership.Status, year int) int {
			if kr, ok := st.PaidByYear[year]; ok {
				return kr
			}
			return -1
		},

		// proposalTone maps a proposal's state onto the badge colours, so the
		// template does not carry a four-way if.
		"proposalTone": func(status store.ProposalStatus) string {
			switch status {
			case store.Approved:
				return "ok"
			case store.Rejected:
				return "bad"
			case store.Pending:
				return "info"
			default:
				return "quiet"
			}
		},

		"siteTagline": func(site config.Site) string { return site.TaglineFor(l) },
		"siteFooter":  func(site config.Site) string { return site.FooterFor(l) },

		// query builds an address with parameters, skipping the empty ones.
		"query": func(base string, pairs ...string) template.URL {
			q := url.Values{}
			for i := 0; i+1 < len(pairs); i += 2 {
				if pairs[i+1] != "" {
					q.Set(pairs[i], pairs[i+1])
				}
			}
			if len(q) == 0 {
				return template.URL(base)
			}
			return template.URL(base + "?" + q.Encode())
		},

		"dict":      dict,
		"hasPrefix": strings.HasPrefix,
		"asset":     s.asset,
	}
}

// asKind narrows whatever a template hands over — a config.Kind, or the bare
// string a form produced — to a membership.
func asKind(v any) config.Kind {
	switch k := v.(type) {
	case config.Kind:
		return k
	case string:
		if parsed, ok := config.ParseKind(k); ok {
			return parsed
		}
	}
	return config.KindBo
}

// dict makes a map inline, which is how a partial is given more than one
// argument.
func dict(pairs ...any) map[string]any {
	m := map[string]any{}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, ok := pairs[i].(string)
		if !ok {
			continue
		}
		m[key] = pairs[i+1]
	}
	return m
}
