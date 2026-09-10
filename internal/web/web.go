// Package web serves the member register: server-rendered HTML, no build
// step, no client-side framework. Everything works without JavaScript; what
// JavaScript there is only makes a long table quicker to search.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/mattermost"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

//go:embed templates/*.html
var templateFS embed.FS

// Patterns by extension rather than the whole directory, so a test file next
// to the scripts stays out of the binary.
//
//go:embed static/*.css static/*.js static/*.png
var staticFS embed.FS

// Server holds everything the handlers need.
type Server struct {
	cfg   *config.Config
	rt    config.Runtime
	store *store.Store
	guard *auth.Guard
	sync  *sync.Syncer
	log   *slog.Logger
	now   func() time.Time

	// chat announces what happened to the two channels the house reads. It is
	// never nil: an unconfigured client logs instead of sending, so no call
	// site has to ask whether Mattermost exists.
	chat *mattermost.Client

	// joinThrottle slows a burst of public form submissions down before it
	// reaches the database. It belongs to the server rather than the package:
	// a limiter shared by every Server in the process is state that leaks
	// between them, which is wrong in a test and would be wrong in a binary
	// that ever ran two.
	joinThrottle *joinLimiter

	// tpl holds one parsed set per language. The language is baked into the
	// template functions, so a page says {{t "key"}} and gets the right words
	// without every call site passing a language around.
	tpl map[i18n.Lang]map[string]*template.Template

	// assets maps a static file to a short content hash, so a new release is
	// fetched rather than served out of a browser cache for another hour.
	assets map[string]string
}

// pages are the top-level templates. Each is parsed into its own set together
// with the shared layout, because every page defines a "content" block and Go
// templates share one namespace per set.
var pages = []string{
	"index.html", "login.html", "error.html", "member.html", "new.html",
	"payments.html", "proposals.html", "admin.html",
	"join.html", "thanks.html", "pipeline.html", "candidate_new.html",
}

// layouts are included in every page set.
var layouts = []string{"base.html", "fields.html", "panels.html"}

// New builds the HTTP server.
func New(cfg *config.Config, rt config.Runtime, st *store.Store, guard *auth.Guard,
	syncer *sync.Syncer, chat *mattermost.Client, log *slog.Logger) (*Server, error) {

	if chat == nil {
		// A disabled client rather than a nil one, so that notify.go never
		// needs a nil check and forgetting one cannot panic a request.
		chat = mattermost.New("", "", log)
	}
	s := &Server{cfg: cfg, rt: rt, store: st, guard: guard, sync: syncer,
		chat: chat, log: log, now: time.Now, joinThrottle: newJoinThrottle()}
	var err error
	if s.assets, err = hashAssets(); err != nil {
		return nil, err
	}
	s.tpl = make(map[i18n.Lang]map[string]*template.Template, len(i18n.Langs))
	for _, lang := range i18n.Langs {
		set := make(map[string]*template.Template, len(pages))
		for _, page := range pages {
			files := make([]string, 0, len(layouts)+1)
			for _, l := range layouts {
				files = append(files, "templates/"+l)
			}
			files = append(files, "templates/"+page)
			t, err := template.New(page).Funcs(s.funcs(lang)).ParseFS(templateFS, files...)
			if err != nil {
				return nil, fmt.Errorf("parse template %s (%s): %w", page, lang, err)
			}
			set[page] = t
		}
		s.tpl[lang] = set
	}
	return s, nil
}

// Handler returns the router with middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", cacheStatic(http.FileServer(http.FS(sub)))))

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok\n"))
	})

	// --- the only pages anybody on the internet may see ---
	mux.HandleFunc("GET /bli-medlem", s.handleJoinForm)
	mux.HandleFunc("POST /bli-medlem", s.handleJoin)
	mux.HandleFunc("GET /bli-medlem/tack", s.handleJoinThanks)
	mux.HandleFunc("GET /bli-medlem/tack/{token}", s.handleJoinThanks)

	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("GET /logga-in", s.handleSignInStart)
	mux.HandleFunc("GET /oauth2/callback", s.handleCallback)
	mux.HandleFunc("POST /demo-inloggning", s.handleDemoSignIn)
	mux.HandleFunc("POST /logga-ut", s.handleSignOut)
	mux.HandleFunc("POST /sprak", s.handleLanguage)

	// --- the register ---
	mux.Handle("GET /{$}", s.page(s.handleRegister))
	mux.Handle("GET /export.csv", s.page(s.handleExport))
	mux.Handle("GET /medlem/ny", s.can(config.PermAdd, s.handleNewForm))
	mux.Handle("POST /medlem/ny", s.can(config.PermAdd, s.handleCreate))
	mux.Handle("GET /medlem/{id}", s.page(s.handleMember))
	mux.Handle("POST /medlem/{id}", s.page(s.handleSave))
	mux.Handle("POST /medlem/{id}/ta-bort", s.page(s.handleDelete))
	mux.Handle("POST /medlem/{id}/betalning", s.can(config.PermPay, s.handlePay))
	mux.Handle("POST /medlem/{id}/angra-betalning", s.can(config.PermPay, s.handleUnpay))

	// --- the cashiers' page ---
	mux.Handle("GET /avgifter", s.page(s.handlePayments))

	// --- the interview team's board ---
	// Reading the board and working it are different permissions: the
	// interview team owns the process, and the board watching is welcome
	// where the board reaching in and moving somebody is not.
	mux.Handle("GET /kandidater", s.can(config.PermPipeline, s.handleCandidates))
	mux.Handle("GET /kandidater/ny", s.can(config.PermPipelineEdit, s.handleNewCandidateForm))
	mux.Handle("POST /kandidater/ny", s.can(config.PermPipelineEdit, s.handleAddCandidate))
	mux.Handle("POST /kandidater/{id}/falt", s.can(config.PermPipelineEdit, s.handleCandidateField))
	mux.Handle("POST /kandidater/{id}", s.can(config.PermPipelineEdit, s.handleSaveCandidate))
	mux.Handle("POST /kandidater/{id}/valkomna", s.can(config.PermPipelineEdit, s.handleWelcomeCandidate))
	mux.Handle("POST /kandidater/{id}/ta-bort", s.can(config.PermPipelineEdit, s.handleDeleteCandidate))
	mux.Handle("POST /kandidater/rensa", s.can(config.PermPipelineEdit, s.handleClearStage))

	// --- the board's approval queue ---
	mux.Handle("GET /andringar", s.page(s.handleProposals))
	mux.Handle("POST /andringar/{id}/godkann", s.can(config.PermApprove, s.handleApprove))
	mux.Handle("POST /andringar/{id}/avsla", s.can(config.PermApprove, s.handleReject))
	mux.Handle("POST /andringar/{id}/aterta", s.page(s.handleWithdraw))

	// --- the things nobody opens during a normal week ---
	mux.Handle("GET /admin", s.page(s.handleAdmin))
	mux.Handle("POST /synk/kor", s.can(config.PermSync, s.handleSyncNow))

	// The two pages these used to be. Banners and old bookmarks point here,
	// and a redirect costs nothing next to a link that has stopped working.
	mux.Handle("GET /synk", s.page(func(w http.ResponseWriter, r *http.Request, v *view) {
		http.Redirect(w, r, "/admin?flik=synk", http.StatusMovedPermanently)
	}))
	mux.Handle("GET /logg", s.can(config.PermApprove, func(w http.ResponseWriter, r *http.Request, v *view) {
		http.Redirect(w, r, "/admin?flik=logg", http.StatusMovedPermanently)
	}))

	return s.recoverPanic(securityHeaders(mux))
}

// handler is a page that needs a signed-in session.
type handler func(http.ResponseWriter, *http.Request, *view)

// page wraps a handler so only a signed-in account reaches it.
func (s *Server) page(h handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.guard.Session(r)
		if !ok {
			http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
			return
		}
		v, err := s.newView(r, w, session)
		if err != nil {
			s.log.Error("could not build the page", "path", r.URL.Path, "err", err)
			s.errorPage(w, r, http.StatusInternalServerError, "error.wentwrong", "error.wentwrong.how")
			return
		}
		h(w, r, v)
	})
}

// can wraps a handler so only a role holding a permission reaches it.
func (s *Server) can(p config.Permission, h handler) http.Handler {
	return s.page(func(w http.ResponseWriter, r *http.Request, v *view) {
		if !s.rt.Access.May(v.Role, p) {
			s.denied(w, r, v, p)
			return
		}
		h(w, r, v)
	})
}

// denied explains a refusal in terms of who *can* do the thing, because the
// answer to "why can't I?" here is always "that is somebody else's job", and
// naming them saves a round of messages.
func (s *Server) denied(w http.ResponseWriter, r *http.Request, v *view, p config.Permission) {
	var who []string
	for _, role := range config.Roles {
		if s.rt.Access.May(role, p) {
			if address := s.rt.AccountFor(role); address != "" {
				who = append(who, address)
			}
		}
	}
	detail := i18n.T(v.Lang, "error.denied.nobody")
	if len(who) > 0 {
		detail = i18n.T(v.Lang, "error.denied.ask", i18n.JoinAnd(v.Lang, who))
	}
	s.renderError(w, r, http.StatusForbidden, i18n.T(v.Lang, "error.denied"), detail)
}

// view is the data every page shares.
type view struct {
	Site config.Site
	Cfg  *config.Config
	// Lang is the language this page is rendered in, Other the one the switch
	// in the top bar moves to, and Here the address to come back to after
	// switching.
	Lang    i18n.Lang
	Other   i18n.Lang
	Here    string
	Session auth.Session
	Role    config.Role
	Now     time.Time
	Loc     *time.Location
	Path    string
	Title   string
	Demo    bool

	// Alarm is what is currently wrong with the synchronisation, summarised
	// for the banner that sits on every page.
	Alarm Alarm
	// Pending is how many proposals are waiting for the board.
	Pending int
	// Overdue is how many members owe this year's fee past their due date.
	Overdue int
	// Applying is how many people outside the association are waiting to hear
	// back. It is on the badge in the top bar, because somebody who has sent
	// a form and heard nothing is the worst thing this register can do.
	Applying int

	// SheetURL is the cashiers' spreadsheet, for the link in the top bar.
	SheetURL string

	Flash     string
	FlashKind string
	Data      any

	access config.Access
}

// May reports whether the signed-in role may do something. Templates call it
// by name — {{if .May "pay"}} — so a button and the handler behind it cannot
// disagree about who is allowed to press it.
func (v *view) May(p string) bool { return v.access.May(v.Role, config.Permission(p)) }

// NeedsApproval reports whether this role's attempt at something would become
// a proposal rather than happening. It is what lets the edit form say "the
// board will be asked" on the button itself.
func (v *view) NeedsApproval(p string) bool {
	return v.access.NeedsApproval(v.Role, config.Permission(p))
}

// Alarm is the standing state of the synchronisation, for the banner.
type Alarm struct {
	// Off means nothing is being synchronised at all, because no service
	// account is configured. That is a warning, not an alarm: a registry
	// without Google still keeps the register.
	Off bool
	// Loud is the number of things that have been wrong long enough to
	// deserve shouting about.
	Loud int
	// Total is everything currently out of step, including the recent.
	Total int
	// Worst is the first thing to fix, named, so the banner can say which
	// address rather than only how many.
	Worst string
}

// Bad reports whether the banner should be red.
func (a Alarm) Bad() bool { return a.Loud > 0 }

func (s *Server) newView(r *http.Request, w http.ResponseWriter, session auth.Session) (*view, error) {
	lang := i18n.FromRequest(r, s.defaultLang())
	flash, kind := s.takeFlash(w, r)
	v := &view{
		Site:      s.cfg.Site,
		Cfg:       s.cfg,
		Lang:      lang,
		Other:     lang.Other(),
		Here:      r.URL.RequestURI(),
		Session:   session,
		Role:      session.Role,
		Now:       s.now().In(s.cfg.Location()),
		Loc:       s.cfg.Location(),
		Path:      r.URL.Path,
		Demo:      s.rt.Demo,
		SheetURL:  s.sheetURL(),
		Flash:     flash,
		FlashKind: kind,
		access:    s.rt.Access,
	}

	ctx := r.Context()
	var err error
	if v.Pending, err = s.store.CountPending(ctx); err != nil {
		return nil, fmt.Errorf("count the proposals: %w", err)
	}
	if v.Alarm, err = s.alarm(ctx); err != nil {
		return nil, err
	}
	if v.Overdue, err = s.countOverdue(ctx); err != nil {
		return nil, err
	}
	// Only for the people who can act on it; nobody else needs the number.
	if s.rt.Access.May(session.Role, config.PermPipeline) {
		open := make([]string, 0, len(s.cfg.Pipeline.Stages))
		for _, st := range s.cfg.Pipeline.Open() {
			open = append(open, st.ID)
		}
		if v.Applying, err = s.store.CountCandidatesIn(ctx, open); err != nil {
			return nil, fmt.Errorf("count the candidates: %w", err)
		}
	}
	return v, nil
}

// alarm summarises the synchronisation for the banner.
func (s *Server) alarm(ctx context.Context) (Alarm, error) {
	if !s.sync.Enabled() {
		return Alarm{Off: true}, nil
	}
	trouble, err := s.sync.Trouble(ctx, s.store)
	if err != nil {
		return Alarm{}, fmt.Errorf("read the sync state: %w", err)
	}
	a := Alarm{Total: len(trouble)}
	for _, t := range trouble {
		if t.Loud {
			a.Loud++
			if a.Worst == "" {
				a.Worst = t.State.Address
				if a.Worst == "" {
					a.Worst = t.State.Subject()
				}
			}
		}
	}
	return a, nil
}

// countOverdue is the number the amber banner shows. It is derived from the
// same code the payments page uses, so the two can never disagree.
func (s *Server) countOverdue(ctx context.Context) (int, error) {
	roster, err := s.roster(ctx)
	if err != nil {
		return 0, err
	}
	return roster.Count().Overdue, nil
}

// roster reads the whole register with everything derived. It is two queries
// and a few hundred rows, which is cheap enough to do per request and saves
// every page from assembling its own half of the picture.
func (s *Server) roster(ctx context.Context) (membership.Roster, error) {
	loc := s.cfg.Location()
	members, err := s.store.Members(ctx, loc)
	if err != nil {
		return membership.Roster{}, fmt.Errorf("read the register: %w", err)
	}
	payments, err := s.store.AllPayments(ctx, loc)
	if err != nil {
		return membership.Roster{}, fmt.Errorf("read the payments: %w", err)
	}
	return membership.Build(members, payments, s.cfg, s.now()), nil
}

// sheetURL is the cashiers' spreadsheet, or empty when there is none. It is
// on every page: it is the one thing here that lives somewhere else, and
// hunting for the tab in Drive is a small tax paid over and over.
func (s *Server) sheetURL() string {
	if !s.cfg.Sheet.Enabled() {
		return ""
	}
	return "https://docs.google.com/spreadsheets/d/" + s.cfg.Sheet.ID + "/edit"
}

// defaultLang is the language a visitor gets before choosing one.
func (s *Server) defaultLang() i18n.Lang {
	lang, _ := i18n.Parse(s.cfg.Site.Language)
	return lang
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, name string, v *view) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	set, ok := s.tpl[v.Lang]
	if !ok {
		set = s.tpl[i18n.Default]
	}
	t, ok := set[name]
	if !ok {
		s.log.Error("unknown template", "template", name)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// Render into a buffer, so a failure halfway through cannot emit half a
	// page with a 200 already on the wire.
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, name, v); err != nil {
		s.log.Error("render template", "template", name, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(status)
	buf.WriteTo(w)
}

// errorPage shows one of the catalogue's error pages. Taking keys rather than
// sentences is what keeps the last few Swedish strings out of the handlers.
func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, status int, headlineKey, detailKey string) {
	lang := i18n.FromRequest(r, s.defaultLang())
	detail := ""
	if detailKey != "" {
		detail = i18n.T(lang, detailKey)
	}
	s.renderError(w, r, status, i18n.T(lang, headlineKey), detail)
}

func (s *Server) renderError(w http.ResponseWriter, r *http.Request, status int, headline, detail string) {
	lang := i18n.FromRequest(r, s.defaultLang())
	session, _ := s.guard.Session(r)
	// Deliberately not newView: whatever went wrong may well be the database,
	// and an error page that needs three queries to render is no error page.
	v := &view{
		Site: s.cfg.Site, Cfg: s.cfg, Lang: lang, Other: lang.Other(),
		Here: r.URL.RequestURI(), Session: session, Role: session.Role,
		Now: s.now().In(s.cfg.Location()), Loc: s.cfg.Location(), Path: r.URL.Path,
		Demo: s.rt.Demo, Title: headline, access: s.rt.Access,
		Data: map[string]any{"Headline": headline, "Detail": detail, "Status": status},
	}
	s.render(w, r, status, "error.html", v)
}

// --- flash messages -------------------------------------------------------

const flashCookie = "rb_flash"

// flash leaves a one-shot message for the page the browser is about to be
// redirected to. It is a plain cookie rather than a session in the database:
// it holds a catalogue key, never anything private, and the worst a forged
// one can do is congratulate somebody on a thing that did not happen.
func (s *Server) flash(w http.ResponseWriter, kind, key string, args ...string) {
	value := kind + "|" + key
	for _, a := range args {
		value += "|" + base64.RawURLEncoding.EncodeToString([]byte(a))
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: value, Path: "/", MaxAge: 60,
		HttpOnly: true, Secure: s.rt.Secure(), SameSite: http.SameSiteLaxMode,
	})
}

// takeFlash reads and clears the message.
func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) (message, kind string) {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return "", ""
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.rt.Secure(), SameSite: http.SameSiteLaxMode})

	parts := strings.Split(c.Value, "|")
	if len(parts) < 2 {
		return "", ""
	}
	lang := i18n.FromRequest(r, s.defaultLang())
	args := make([]any, 0, len(parts)-2)
	for _, p := range parts[2:] {
		raw, err := base64.RawURLEncoding.DecodeString(p)
		if err != nil {
			return "", ""
		}
		args = append(args, string(raw))
	}
	if !i18n.Has(parts[1]) {
		return "", ""
	}
	return i18n.T(lang, parts[1], args...), parts[0]
}

// --- static assets --------------------------------------------------------

// hashAssets fingerprints the embedded static files. They live in the binary,
// so this happens once at startup and cannot change afterwards.
func hashAssets() (map[string]string, error) {
	out := map[string]string{}
	entries, err := fs.ReadDir(staticFS, "static")
	if err != nil {
		return nil, fmt.Errorf("read static files: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := staticFS.ReadFile("static/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read static/%s: %w", e.Name(), err)
		}
		sum := sha256.Sum256(raw)
		out[e.Name()] = hex.EncodeToString(sum[:])[:10]
	}
	return out, nil
}

// asset returns the URL for a static file, stamped with its content hash.
// Browsers may then cache it for as long as they like: changed content has a
// changed address, so nobody is left running last week's stylesheet.
func (s *Server) asset(name string) string {
	if sum, ok := s.assets[name]; ok {
		return "/static/" + name + "?v=" + sum
	}
	return "/static/" + name
}

// --- middleware -----------------------------------------------------------

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		// Everything is served from this origin; no external scripts or
		// styles, and the register is never framed anywhere.
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self'; form-action 'self' https://accounts.google.com; "+
				"frame-ancestors 'none'; base-uri 'none'")
		next.ServeHTTP(w, r)
	})
}

// cacheStatic lets browsers keep static files. A request carrying a ?v= hash
// is answered as immutable, because that exact content will never change; a
// bare request gets a short cache instead, so a stale copy cannot outlive the
// day.
func cacheStatic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=300")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverPanic(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic serving request", "path", r.URL.Path, "err", rec)
				s.errorPage(w, r, http.StatusInternalServerError, "error.wentwrong", "error.wentwrong.how")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the caller's address, honouring X-Forwarded-For when the
// deployment sits behind a reverse proxy.
func (s *Server) clientIP(r *http.Request) string {
	if s.rt.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			if i := strings.IndexByte(xff, ','); i >= 0 {
				return strings.TrimSpace(xff[:i])
			}
			return strings.TrimSpace(xff)
		}
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

// safeNext keeps redirects on this site.
func safeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/"
	}
	return next
}
