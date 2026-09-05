package web

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
)

// signInThrottle slows down anybody hammering the sign-in from one address.
// Google does the real work of authenticating; this only stops the callback
// from being used as a way to make us call Google over and over.
type signInThrottle struct {
	mu     sync.Mutex
	tries  map[string][]time.Time
	window time.Duration
	maxTry int
}

func newThrottle() *signInThrottle {
	return &signInThrottle{tries: map[string][]time.Time{}, window: 15 * time.Minute, maxTry: 20}
}

func (t *signInThrottle) allow(ip string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	cut := now.Add(-t.window)
	kept := t.tries[ip][:0]
	for _, at := range t.tries[ip] {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	t.tries[ip] = append(kept, now)
	return len(t.tries[ip]) <= t.maxTry
}

var throttle = newThrottle()

// handleLoginPage is the front door.
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.guard.Session(r); ok {
		http.Redirect(w, r, safeNext(r.URL.Query().Get("next")), http.StatusSeeOther)
		return
	}
	lang := i18n.FromRequest(r, s.defaultLang())
	v := &view{
		Site: s.cfg.Site, Cfg: s.cfg, Lang: lang, Other: lang.Other(),
		Here: r.URL.RequestURI(), Now: s.now().In(s.cfg.Location()), Loc: s.cfg.Location(),
		Path: r.URL.Path, Demo: s.rt.Demo, access: s.rt.Access,
		Title: i18n.T(lang, "login.title"),
	}
	v.Data = map[string]any{
		"Next":  r.URL.Query().Get("next"),
		"Error": s.signInError(lang, r.URL.Query().Get("fel")),
		// The demo hands out roles for the asking, so the page needs to know
		// which ones there are and what each of them may do.
		"Roles":  s.demoRoles(),
		"Domain": s.rt.Google.HostedDomain,
	}
	s.render(w, r, http.StatusOK, "login.html", v)
}

// demoRole is one of the three parts a demo visitor can try on.
type demoRole struct {
	Role    config.Role
	Email   string
	NameKey string
	CanKey  string
}

func (s *Server) demoRoles() []demoRole {
	if !s.rt.Demo {
		return nil
	}
	out := make([]demoRole, 0, len(config.Roles))
	for _, role := range config.Roles {
		address := s.rt.AccountFor(role)
		if address == "" {
			continue
		}
		out = append(out, demoRole{
			Role:    role,
			Email:   address,
			NameKey: "role." + string(role),
			CanKey:  "role." + string(role) + ".can",
		})
	}
	return out
}

// signInError turns the code carried in the query string back into a
// sentence. The code goes through the URL rather than the message itself, so
// that nothing on the sign-in page can be dictated by whoever wrote the link.
func (s *Server) signInError(lang i18n.Lang, code string) string {
	switch code {
	case "":
		return ""
	case "notallowed":
		return i18n.T(lang, "login.notallowed", strings.Join(s.accountList(), ", "))
	case "domain":
		return i18n.T(lang, "login.wrongdomain", s.rt.Google.HostedDomain)
	case "throttled":
		return i18n.T(lang, "login.throttled")
	default:
		return i18n.T(lang, "login.failed")
	}
}

// accountList is the addresses that may sign in, for the sign-in page. It is
// the register's own configuration rather than a hard-coded sentence, so
// moving an account is one environment variable and not a code change.
func (s *Server) accountList() []string {
	var out []string
	for _, role := range config.Roles {
		if address := s.rt.AccountFor(role); address != "" {
			out = append(out, address)
		}
	}
	return out
}

func (s *Server) handleSignInStart(w http.ResponseWriter, r *http.Request) {
	if s.rt.Demo {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	if !throttle.allow(s.clientIP(r), s.now()) {
		http.Redirect(w, r, "/login?fel=throttled", http.StatusSeeOther)
		return
	}
	where, err := s.guard.Start(w, safeNext(r.URL.Query().Get("next")))
	if err != nil {
		s.log.Error("could not start a sign-in", "err", err)
		http.Redirect(w, r, "/login?fel=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, where, http.StatusSeeOther)
}

func (s *Server) handleCallback(w http.ResponseWriter, r *http.Request) {
	if s.rt.Demo {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	session, next, err := s.guard.Finish(r.Context(), w, r)
	if err != nil {
		code := "failed"
		switch {
		case errors.Is(err, auth.ErrNotAllowed):
			code = "notallowed"
		case strings.Contains(err.Error(), "may sign in"):
			code = "domain"
		}
		s.log.Warn("sign-in refused", "ip", s.clientIP(r), "err", err)
		http.Redirect(w, r, "/login?fel="+code, http.StatusSeeOther)
		return
	}
	s.guard.Issue(w, session.Email, session.Name)
	s.log.Info("signed in", "account", session.Email, "role", session.Role, "ip", s.clientIP(r))
	http.Redirect(w, r, safeNext(next), http.StatusSeeOther)
}

// handleDemoSignIn is the demo's whole authentication story: pick a role and
// you are it. It exists only when DEMO is set, and refuses outright
// otherwise, so a misconfigured production instance cannot be walked into.
func (s *Server) handleDemoSignIn(w http.ResponseWriter, r *http.Request) {
	if !s.rt.Demo {
		s.errorPage(w, r, http.StatusNotFound, "error.nopage", "")
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	role := config.Role(r.FormValue("roll"))
	address := s.rt.AccountFor(role)
	if !role.LoggedIn() || address == "" {
		http.Redirect(w, r, "/login?fel=notallowed", http.StatusSeeOther)
		return
	}
	s.guard.Issue(w, address, "Demo: "+role.Label())
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

func (s *Server) handleSignOut(w http.ResponseWriter, r *http.Request) {
	s.guard.Clear(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleLanguage remembers which language to serve and returns the reader to
// the page they were on. It is a form rather than a link so that following it
// cannot be prefetched into a change nobody asked for.
func (s *Server) handleLanguage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	lang, ok := i18n.Parse(r.FormValue("lang"))
	if !ok {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	i18n.SetCookie(w, lang, s.rt.Secure())
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}
