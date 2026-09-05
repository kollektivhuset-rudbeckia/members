// Package i18n holds every word the site says to a person, in both languages.
//
// The two translations of a phrase sit side by side in one catalogue rather
// than in separate files per language. With two languages that is the layout
// that makes a missing or drifting translation obvious at a glance, and a test
// walks the templates to prove no key is missing in either column.
package i18n

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Lang is a language the site is served in.
type Lang string

const (
	SV Lang = "sv"
	EN Lang = "en"
)

// Langs are the languages on offer, in the order the switch cycles them.
var Langs = []Lang{SV, EN}

// Default is what a visitor gets before saying otherwise.
const Default = SV

// Name is the language's own name for itself, for the switch.
func (l Lang) Name() string {
	switch l {
	case EN:
		return "English"
	}
	return "Svenska"
}

// Code is the short label on the switch.
func (l Lang) Code() string { return strings.ToUpper(string(l)) }

// Other is the language the switch moves to.
func (l Lang) Other() Lang {
	if l == SV {
		return EN
	}
	return SV
}

// Parse reads a stored or submitted language.
func Parse(s string) (Lang, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "sv":
		return SV, true
	case "en":
		return EN, true
	}
	return Default, false
}

// Cookie is where a visitor's choice is kept. It sits beside rb_session and
// rb_ident, and like them it is signed by nothing: a language is not a
// permission, so the worst a tampered value can do is fall back to Swedish.
const Cookie = "rb_lang"

// FromRequest works out which language to serve: the visitor's own choice
// first, then what their browser asks for, then the deployment's default.
func FromRequest(r *http.Request, fallback Lang) Lang {
	if c, err := r.Cookie(Cookie); err == nil {
		if lang, ok := Parse(c.Value); ok {
			return lang
		}
	}
	if lang, ok := fromAcceptLanguage(r.Header.Get("Accept-Language")); ok {
		return lang
	}
	if fallback == "" {
		return Default
	}
	return fallback
}

// fromAcceptLanguage picks the highest-weighted language we actually have.
// Anything unknown is ignored rather than treated as a vote for the default,
// so a browser asking for "de, en" gets English.
func fromAcceptLanguage(header string) (Lang, bool) {
	type option struct {
		lang Lang
		q    float64
	}
	var options []option
	for _, part := range strings.Split(header, ",") {
		fields := strings.Split(strings.TrimSpace(part), ";")
		tag := strings.ToLower(strings.TrimSpace(fields[0]))
		if i := strings.IndexByte(tag, '-'); i > 0 {
			tag = tag[:i]
		}
		lang, ok := Parse(tag)
		if !ok {
			continue
		}
		q := 1.0
		for _, f := range fields[1:] {
			f = strings.TrimSpace(f)
			if !strings.HasPrefix(f, "q=") {
				continue
			}
			if _, err := fmt.Sscanf(f[2:], "%f", &q); err != nil {
				q = 1.0
			}
		}
		options = append(options, option{lang, q})
	}
	if len(options) == 0 {
		return Default, false
	}
	sort.SliceStable(options, func(i, j int) bool { return options[i].q > options[j].q })
	return options[0].lang, true
}

// SetCookie remembers the visitor's choice for a year.
func SetCookie(w http.ResponseWriter, lang Lang, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     Cookie,
		Value:    string(lang),
		Path:     "/",
		Expires:  time.Now().AddDate(1, 0, 0),
		MaxAge:   365 * 24 * 3600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// T looks up a phrase. Any arguments are substituted with fmt.Sprintf, so a
// phrase with a %s or %d in it can be assembled in either language's word
// order.
func T(lang Lang, key string, args ...any) string {
	e, ok := catalog[key]
	if !ok {
		// Loud rather than silent: a missing phrase is a bug, and showing the
		// key is how it gets noticed and fixed.
		return "⟦" + key + "⟧"
	}
	s := e.sv
	if lang == EN {
		s = e.en
	}
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Has reports whether a key exists, for the test that guards the catalogue.
func Has(key string) bool {
	_, ok := catalog[key]
	return ok
}

// Keys lists every phrase in the catalogue.
func Keys() []string {
	out := make([]string, 0, len(catalog))
	for k := range catalog {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Entry returns both translations of a key.
func Entry(key string) (sv, en string, ok bool) {
	e, found := catalog[key]
	return e.sv, e.en, found
}
