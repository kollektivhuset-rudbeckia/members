package web

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

// The pipeline is the interview team's own working board, and it is theirs:
// ny@ and the board see it, nobody else. It carries what people said about
// themselves before the association agreed to anything, which is not the
// cashier's business.

// column is one stage with the people in it.
type column struct {
	Stage      config.Stage
	Name       string
	Candidates []candidateView
}

// candidateView is a candidate with what the register already knows.
type candidateView struct {
	Candidate store.Candidate
	// Member is set when this person is already in the register, either
	// because they were welcomed or because somebody wrote them down
	// separately. Welcoming them twice would make a duplicate.
	Member store.Member
	Known  bool
}

func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request, v *view) {
	list, err := s.store.Candidates(r.Context(), v.Loc)
	if err != nil {
		s.log.Error("could not read the pipeline", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}

	// Fold the closed stages away unless asked for. They are history rather
	// than work, and there are three times as many of them.
	showClosed := r.URL.Query().Get("visa") == "alla"

	byStage := map[string][]candidateView{}
	for _, c := range list {
		cv := candidateView{Candidate: c}
		if c.Email != "" {
			if m, err := s.store.MemberByEmail(r.Context(), c.Email, v.Loc); err == nil {
				cv.Member, cv.Known = m, true
			}
		}
		byStage[c.Stage] = append(byStage[c.Stage], cv)
	}

	var columns []column
	waiting, closed := 0, 0
	for _, st := range s.cfg.Pipeline.Stages {
		people := byStage[st.ID]
		if st.Closed {
			closed += len(people)
			if !showClosed {
				continue
			}
		} else {
			waiting += len(people)
		}
		columns = append(columns, column{
			Stage: st, Name: st.NameFor(string(v.Lang)), Candidates: people,
		})
	}
	// A stage that has been taken out of the configuration would otherwise
	// hide the people standing in it.
	for stage, people := range byStage {
		if _, known := s.cfg.Pipeline.Stage(stage); !known {
			columns = append(columns, column{
				Stage: config.Stage{ID: stage}, Name: stage, Candidates: people,
			})
		}
	}

	v.Title = i18n.T(v.Lang, "pipeline.title")
	v.Data = map[string]any{
		"Columns":    columns,
		"Stages":     s.cfg.Pipeline.Stages,
		"Waiting":    waiting,
		"ClosedSeen": showClosed,
		"ClosedN":    closed,
		"Kinds":      s.kindOptions(v.Lang),
		"JoinURL":    s.rt.BaseURL + "/bli-medlem",
		"Form":       candidateForm{Kind: string(config.KindVan), Stage: s.cfg.Pipeline.EntryStage()},
	}
	s.render(w, r, http.StatusOK, "pipeline.html", v)
}

// candidateForm is the add-and-edit form for somebody the team met rather
// than somebody who filled the page in.
type candidateForm struct {
	FirstName   string
	LastName    string
	Email       string
	Phone       string
	Apartment   string
	Kind        string
	Stage       string
	Responsible string
	InterviewOn string
	Note        string
	Errors      map[string]string
}

func (f candidateForm) Bad() bool               { return len(f.Errors) > 0 }
func (f candidateForm) Err(field string) string { return f.Errors[field] }

func readCandidateForm(r *http.Request) candidateForm {
	return candidateForm{
		FirstName:   strings.TrimSpace(r.FormValue("fornamn")),
		LastName:    strings.TrimSpace(r.FormValue("efternamn")),
		Email:       store.Email(r.FormValue("epost")),
		Phone:       strings.TrimSpace(r.FormValue("telefon")),
		Apartment:   strings.TrimSpace(r.FormValue("lagenhet")),
		Kind:        strings.TrimSpace(r.FormValue("typ")),
		Stage:       strings.TrimSpace(r.FormValue("steg")),
		Responsible: strings.TrimSpace(r.FormValue("ansvarig")),
		InterviewOn: strings.TrimSpace(r.FormValue("intervju")),
		Note:        strings.TrimSpace(r.FormValue("anteckning")),
	}
}

// handleAddCandidate writes down somebody the team met.
func (s *Server) handleAddCandidate(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	f := readCandidateForm(r)
	if f.FirstName == "" && f.LastName == "" {
		s.flash(w, "error", "form.err.name")
		http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
		return
	}
	kind, ok := config.ParseKind(f.Kind)
	if !ok {
		kind = config.KindVan
	}
	stage := f.Stage
	if _, known := s.cfg.Pipeline.Stage(stage); !known {
		stage = s.cfg.Pipeline.EntryStage()
	}

	now := s.now()
	c := store.Candidate{
		ID: auth.ID(), Token: auth.Token(),
		FirstName: f.FirstName, LastName: f.LastName, Email: f.Email, Phone: f.Phone,
		Apartment: f.Apartment, Kind: kind, Stage: stage,
		Responsible: f.Responsible, Note: f.Note, Source: store.SourceByHand,
		CreatedAt: now, MovedAt: now, MovedBy: v.Session.Email,
	}
	if f.InterviewOn != "" {
		if t, err := store.ParseDay(f.InterviewOn, v.Loc); err == nil {
			c.InterviewOn = sql.NullTime{Time: t, Valid: true}
		}
	}
	if err := s.store.CreateCandidate(r.Context(), c); err != nil {
		s.log.Error("could not add a candidate", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	s.flash(w, "ok", "flash.candidate.added", c.Name())
	http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
}

// handleSaveCandidate moves somebody along, or corrects their details. It is
// one handler because on this board they are the same act: you open a card,
// change what you learned, and put it back.
func (s *Server) handleSaveCandidate(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	c, ok := s.candidate(w, r, v)
	if !ok {
		return
	}
	f := readCandidateForm(r)

	// A move on its own posts only the stage; a full edit posts everything.
	if stage := f.Stage; stage != "" {
		if _, known := s.cfg.Pipeline.Stage(stage); !known {
			s.flash(w, "error", "flash.candidate.nostage")
			http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
			return
		}
		c.Stage = stage
	}
	if r.FormValue("hela") == "1" {
		c.FirstName, c.LastName = f.FirstName, f.LastName
		c.Email, c.Phone, c.Apartment = f.Email, f.Phone, f.Apartment
		c.Responsible, c.Note = f.Responsible, f.Note
		if kind, ok := config.ParseKind(f.Kind); ok {
			c.Kind = kind
		}
		c.InterviewOn = sql.NullTime{}
		if f.InterviewOn != "" {
			if t, err := store.ParseDay(f.InterviewOn, v.Loc); err == nil {
				c.InterviewOn = sql.NullTime{Time: t, Valid: true}
			}
		}
	}
	c.MovedAt, c.MovedBy = s.now(), v.Session.Email

	if err := s.store.UpdateCandidate(r.Context(), c); err != nil {
		s.log.Error("could not save a candidate", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	s.flash(w, "ok", "flash.candidate.saved", c.Name())
	http.Redirect(w, r, s.backTo(r, "/kandidater"), http.StatusSeeOther)
}

// handleWelcomeCandidate turns somebody on the board into a member.
//
// It is the same act as writing a member down by hand, so it needs the same
// permission and nothing more — which is what makes the pipeline useful to
// the team that already has it.
func (s *Server) handleWelcomeCandidate(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	c, ok := s.candidate(w, r, v)
	if !ok {
		return
	}
	if c.Email == "" {
		s.flash(w, "error", "flash.candidate.noemail")
		http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
		return
	}

	// Somebody may already be in the register, written down separately.
	if existing, err := s.store.MemberByEmail(r.Context(), c.Email, v.Loc); err == nil {
		c.MemberID, c.MovedAt, c.MovedBy = existing.ID, s.now(), v.Session.Email
		if closed := s.welcomedStage(); closed != "" {
			c.Stage = closed
		}
		if err := s.store.UpdateCandidate(r.Context(), c); err != nil {
			s.log.Error("could not close a candidate", "err", err)
		}
		s.flash(w, "warn", "flash.alreadymember", existing.Name())
		http.Redirect(w, r, "/medlem/"+existing.ID, http.StatusSeeOther)
		return
	}

	kind := c.Kind
	if raw := r.FormValue("typ"); raw != "" {
		if parsed, ok := config.ParseKind(raw); ok {
			kind = parsed
		}
	}
	now := s.now()
	m := store.Member{
		ID: auth.ID(), FirstName: c.FirstName, LastName: c.LastName,
		Email: c.Email, Phone: c.Phone, Kind: kind, Apartment: c.Apartment,
		// From today rather than from when they first wrote: the association
		// counts membership from when it said yes, and the queue points that
		// follow should not be backdated by a slow inbox.
		JoinedOn:  now,
		Note:      c.Note,
		CreatedAt: now, CreatedBy: v.Session.Email,
		UpdatedAt: now, UpdatedBy: v.Session.Email,
	}
	if err := s.store.CreateMember(r.Context(), m); err != nil {
		if errors.Is(err, store.ErrDuplicateEmail) {
			s.flash(w, "error", "flash.emailtaken", m.Email)
			http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
			return
		}
		s.log.Error("could not welcome a candidate", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}

	c.MemberID, c.MovedAt, c.MovedBy = m.ID, now, v.Session.Email
	if closed := s.welcomedStage(); closed != "" {
		c.Stage = closed
	}
	if err := s.store.UpdateCandidate(r.Context(), c); err != nil {
		s.log.Error("could not close a candidate", "err", err)
	}

	s.audit(r.Context(), v, "member.added", m, "från kandidatlistan")
	s.sync.Nudge(sync.TriggerMember)
	s.flash(w, "ok", "flash.welcomed", m.Name())
	s.log.Info("a candidate became a member", "id", m.ID, "email", m.Email, "by", v.Session.Email)
	http.Redirect(w, r, "/medlem/"+m.ID, http.StatusSeeOther)
}

// welcomedStage is the closed stage somebody lands in once they are a member.
func (s *Server) welcomedStage() string {
	for _, st := range s.cfg.Pipeline.Stages {
		if st.Closed && st.ID == "welcomed" {
			return st.ID
		}
	}
	for _, st := range s.cfg.Pipeline.Stages {
		if st.Closed {
			return st.ID
		}
	}
	return ""
}

func (s *Server) candidate(w http.ResponseWriter, r *http.Request, v *view) (store.Candidate, bool) {
	c, err := s.store.Candidate(r.Context(), r.PathValue("id"), v.Loc)
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound, "error.nocandidate", "")
		return c, false
	}
	if err != nil {
		s.log.Error("could not read a candidate", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return c, false
	}
	return c, true
}
