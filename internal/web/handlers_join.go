package web

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/payment"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// This is the only part of the register anybody on the internet can reach, so
// it is the only part that needs to be careful about who is asking. Three
// things guard it, and none of them is a captcha:
//
//   - a limit per address, kept in the database rather than in memory so that
//     a restart is not a way around it;
//   - a field no person can see and every naive robot fills in;
//   - a refusal to file a second application for an address that already has
//     one waiting, which is mostly about somebody pressing the button twice.
//
// None of it stops a determined person. It does not have to: the worst it
// costs the association is somebody at ny@ pressing "decline", and a captcha
// would cost every genuine applicant instead.

const (
	// applicationsPerDay is how many one address may send.
	applicationsPerDay = 5
	// applicationWindow is what "lately" means for that count.
	applicationWindow = 24 * time.Hour
)

// joinThrottle slows a burst down before it reaches the database at all.
var joinThrottle = newJoinThrottle()

type joinLimiter struct {
	mu    sync.Mutex
	seen  map[string][]time.Time
	limit int
	every time.Duration
}

func newJoinThrottle() *joinLimiter {
	return &joinLimiter{seen: map[string][]time.Time{}, limit: 10, every: time.Hour}
}

func (l *joinLimiter) allow(ip string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cut := now.Add(-l.every)
	kept := l.seen[ip][:0]
	for _, at := range l.seen[ip] {
		if at.After(cut) {
			kept = append(kept, at)
		}
	}
	l.seen[ip] = append(kept, now)
	// Forget everybody else occasionally, so the map cannot grow for ever.
	if len(l.seen) > 5000 {
		for k, times := range l.seen {
			if len(times) == 0 || times[len(times)-1].Before(cut) {
				delete(l.seen, k)
			}
		}
	}
	return len(l.seen[ip]) <= l.limit
}

// joinForm is what somebody outside the association fills in.
type joinForm struct {
	FirstName string
	LastName  string
	Email     string
	Phone     string
	Kind      string
	Message   string
	Errors    map[string]string
}

func (f joinForm) Bad() bool               { return len(f.Errors) > 0 }
func (f joinForm) Err(field string) string { return f.Errors[field] }

func readJoinForm(r *http.Request) joinForm {
	return joinForm{
		FirstName: strings.TrimSpace(r.FormValue("fornamn")),
		LastName:  strings.TrimSpace(r.FormValue("efternamn")),
		Email:     store.Email(r.FormValue("epost")),
		Phone:     strings.TrimSpace(r.FormValue("telefon")),
		Kind:      strings.TrimSpace(r.FormValue("typ")),
		Message:   strings.TrimSpace(r.FormValue("meddelande")),
	}
}

// validate checks the form the way a person would: is there a name, does the
// address look like one, is the message a message rather than an essay.
func (f *joinForm) validate() (config.Kind, bool) {
	f.Errors = map[string]string{}
	if f.FirstName == "" && f.LastName == "" {
		f.Errors["name"] = "form.err.name"
	}
	switch {
	case f.Email == "":
		f.Errors["email"] = "form.err.email.missing"
	case !emailShape.MatchString(f.Email):
		f.Errors["email"] = "form.err.email.shape"
	}
	kind, ok := config.ParseKind(f.Kind)
	if !ok {
		f.Errors["kind"] = "form.err.kind"
	}
	if len([]rune(f.Message)) > 2000 {
		f.Errors["message"] = "join.err.long"
	}
	for _, tooLong := range []struct {
		field string
		value string
	}{{"name", f.FirstName}, {"name", f.LastName}, {"email", f.Email}, {"phone", f.Phone}} {
		if len([]rune(tooLong.value)) > 200 {
			f.Errors[tooLong.field] = "join.err.long"
		}
	}
	return kind, len(f.Errors) == 0
}

// publicView builds the shell for a page nobody has signed in to. It is not
// newView: that reads the register to count what is out of step, and none of
// it belongs on a page for the public.
func (s *Server) publicView(r *http.Request, title string) *view {
	lang := i18n.FromRequest(r, s.defaultLang())
	return &view{
		Site: s.cfg.Site, Cfg: s.cfg, Lang: lang, Other: lang.Other(),
		Here: r.URL.RequestURI(), Now: s.now().In(s.cfg.Location()),
		Loc: s.cfg.Location(), Path: r.URL.Path, Demo: s.rt.Demo,
		access: s.rt.Access, Title: title,
	}
}

func (s *Server) handleJoinForm(w http.ResponseWriter, r *http.Request) {
	s.renderJoin(w, r, joinForm{Kind: string(config.KindVan)}, http.StatusOK)
}

func (s *Server) renderJoin(w http.ResponseWriter, r *http.Request, f joinForm, status int) {
	v := s.publicView(r, i18n.T(i18n.FromRequest(r, s.defaultLang()), "join.title"))
	year := v.Now.Year()
	v.Data = map[string]any{
		"Form":  f,
		"Kinds": s.kindOptions(v.Lang),
		"Year":  year,
		"FeeBo": s.cfg.Membership.FeeFor(config.KindBo, year),
		// What it will be next year, when that has already been decided. A
		// person deciding whether to join deserves to know the fee is going
		// up before they find out by being billed for it.
		"NextYear":   year + 1,
		"FeeNext":    s.cfg.Membership.FeeFor(config.KindBo, year+1),
		"FeeChanges": s.cfg.Membership.FeeFor(config.KindBo, year) != s.cfg.Membership.FeeFor(config.KindBo, year+1),
		"Bankgiro":   s.cfg.Membership.Bankgiro,
		"Swish":      s.cfg.Membership.Swish,
		"Contact":    s.rt.AccountFor(config.RoleIntake),
	}
	s.render(w, r, status, "join.html", v)
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	ip, now := s.clientIP(r), s.now()

	// A field positioned off the page. A person never sees it; a robot that
	// fills every input in the form does. Answer as though it worked: telling
	// it what gave it away only helps the next attempt.
	if strings.TrimSpace(r.FormValue("hemsida")) != "" {
		s.log.Info("a join form with the honeypot filled in was discarded", "ip", ip)
		http.Redirect(w, r, "/bli-medlem/tack", http.StatusSeeOther)
		return
	}

	f := readJoinForm(r)
	kind, ok := f.validate()
	if !ok {
		s.renderJoin(w, r, f, http.StatusUnprocessableEntity)
		return
	}

	if !joinThrottle.allow(ip, now) {
		s.log.Warn("too many join forms from one address", "ip", ip)
		f.Errors = map[string]string{"form": "join.err.toomany"}
		s.renderJoin(w, r, f, http.StatusTooManyRequests)
		return
	}
	if n, err := s.store.RecentCandidatesFrom(r.Context(), ip, now.Add(-applicationWindow)); err == nil &&
		n >= applicationsPerDay {
		s.log.Warn("an address has sent its day's worth of join forms", "ip", ip, "count", n)
		f.Errors = map[string]string{"form": "join.err.toomany"}
		s.renderJoin(w, r, f, http.StatusTooManyRequests)
		return
	}

	// Pressing the button twice should not put somebody on the board twice.
	// Send them to the same page they saw the first time.
	if existing, err := s.store.OpenCandidateByEmail(r.Context(), f.Email,
		s.closedStages(), s.cfg.Location()); err == nil {
		http.Redirect(w, r, "/bli-medlem/tack/"+existing.Token, http.StatusSeeOther)
		return
	}

	c := store.Candidate{
		ID: auth.ID(), Token: auth.Token(),
		FirstName: f.FirstName, LastName: f.LastName, Email: f.Email, Phone: f.Phone,
		Kind: kind, Message: f.Message,
		Stage:     s.cfg.Pipeline.EntryStage(),
		Source:    store.SourceForm,
		CreatedAt: now, CreatedIP: ip, MovedAt: now,
	}
	if err := s.store.CreateCandidate(r.Context(), c); err != nil {
		s.log.Error("could not record an interest", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	s.log.Info("somebody would like to join", "name", c.Name(), "email", c.Email, "kind", c.Kind)
	http.Redirect(w, r, "/bli-medlem/tack/"+c.Token, http.StatusSeeOther)
}

// closedStages are the stages where somebody has stopped moving, which is
// what makes a second form from the same address a new candidate rather than
// a duplicate of an old one.
func (s *Server) closedStages() []string {
	var out []string
	for _, st := range s.cfg.Pipeline.Stages {
		if st.Closed {
			out = append(out, st.ID)
		}
	}
	return out
}

// handleJoinThanks is the page that says what happens next and how to pay. It
// is addressed by an unguessable token, so that applications cannot be read
// by counting, and so that somebody can come back to their own later.
func (s *Server) handleJoinThanks(w http.ResponseWriter, r *http.Request) {
	v := s.publicView(r, i18n.T(i18n.FromRequest(r, s.defaultLang()), "join.thanks.title"))

	token := r.PathValue("token")
	if token == "" {
		// The bare address, which is where a discarded robot submission and
		// anybody typing the URL by hand both land.
		v.Data = map[string]any{"Known": false}
		s.render(w, r, http.StatusOK, "thanks.html", v)
		return
	}

	a, err := s.store.CandidateByToken(r.Context(), token, v.Loc)
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, "join.thanks.gone", "join.thanks.gone.how")
		return
	}
	ways, err := payment.For(s.cfg.Membership, a.Kind, v.Now.Year(), a.Name())
	if err != nil {
		// A QR code that will not encode must not lose somebody the bankgiro.
		s.log.Error("could not build the payment details", "err", err)
		ways = payment.Ways{
			Year: v.Now.Year(), AmountKr: s.cfg.Membership.FeeFor(a.Kind, v.Now.Year()),
			Reference: s.cfg.Membership.Reference(a.Name()), Bankgiro: s.cfg.Membership.Bankgiro,
		}
	}
	v.Data = map[string]any{
		"Known":     true,
		"Candidate": a,
		"Pay":       ways,
		"Contact":   s.rt.AccountFor(config.RoleIntake),
	}
	s.render(w, r, http.StatusOK, "thanks.html", v)
}
