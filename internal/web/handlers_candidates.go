package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
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

	// Every stage gets a lane, in the order the configuration lists them, and
	// they all sit in one row. A board where the finished columns hang below
	// the live ones is not a board.
	var columns []column
	waiting := 0
	for _, st := range s.cfg.Pipeline.Stages {
		people := byStage[st.ID]
		if !st.Closed {
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
		"Columns": columns,
		"Stages":  s.cfg.Pipeline.Stages,
		"Waiting": waiting,
		"Kinds":   s.kindOptions(v.Lang),
		"JoinURL": s.rt.BaseURL + "/bli-medlem",
	}
	s.render(w, r, http.StatusOK, "pipeline.html", v)
}

// handleNewCandidateForm is the page behind the "add" button.
//
// It is a real page rather than only a dialog, so that the button is a link
// that works before any JavaScript has run. With JavaScript the same markup
// is shown in a dialog over the board, because leaving the board to write one
// name down and coming back to find your scroll position gone is a small
// misery repeated all afternoon.
func (s *Server) handleNewCandidateForm(w http.ResponseWriter, r *http.Request, v *view) {
	v.Title = i18n.T(v.Lang, "pipeline.add")
	v.Data = map[string]any{
		"Stages": s.cfg.Pipeline.Stages,
		"Kinds":  s.kindOptions(v.Lang),
		"Entry":  s.cfg.Pipeline.EntryStage(),
	}
	s.render(w, r, http.StatusOK, "candidate_new.html", v)
}

// editable are the fields a click on the board may change, and the only ones.
// Anything else posted to the field endpoint is refused: an allowlist is what
// stops a hand-made request from writing to a column nobody meant to expose.
var editable = map[string]bool{
	"fornamn": true, "efternamn": true, "epost": true, "telefon": true,
	"lagenhet": true, "ansvarig": true, "intervju": true, "anteckning": true,
	"typ": true, "steg": true,
}

// handleCandidateField changes one field of one candidate.
//
// One field, because that is what a click on the board edits. The whole form
// still exists on the candidate's own page for anybody without JavaScript,
// but nobody should have to open eleven inputs to correct a telephone number.
func (s *Server) handleCandidateField(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	c, ok := s.candidate(w, r, v)
	if !ok {
		return
	}
	field := strings.TrimSpace(r.FormValue("falt"))
	value := strings.TrimSpace(r.FormValue("varde"))
	if !editable[field] {
		http.Error(w, "no such field", http.StatusBadRequest)
		return
	}
	if len([]rune(value)) > 500 {
		http.Error(w, "too long", http.StatusRequestEntityTooLarge)
		return
	}

	switch field {
	case "fornamn":
		c.FirstName = value
	case "efternamn":
		c.LastName = value
	case "epost":
		c.Email = store.Email(value)
	case "telefon":
		c.Phone = value
	case "lagenhet":
		c.Apartment = value
	case "ansvarig":
		c.Responsible = value
	case "anteckning":
		c.Note = value
	case "typ":
		kind, ok := config.ParseKind(value)
		if !ok {
			http.Error(w, "no such membership", http.StatusBadRequest)
			return
		}
		c.Kind = kind
	case "steg":
		if _, known := s.cfg.Pipeline.Stage(value); !known {
			http.Error(w, "no such stage", http.StatusBadRequest)
			return
		}
		c.Stage = value
	case "intervju":
		c.InterviewOn = sql.NullTime{}
		if value != "" {
			t, err := store.ParseDay(value, v.Loc)
			if err != nil {
				http.Error(w, "not a date", http.StatusBadRequest)
				return
			}
			c.InterviewOn = sql.NullTime{Time: t, Valid: true}
		}
	}
	c.MovedAt, c.MovedBy = s.now(), v.Session.Email

	if err := s.store.UpdateCandidate(r.Context(), c); err != nil {
		s.log.Error("could not change a candidate", "err", err)
		http.Error(w, "could not save", http.StatusInternalServerError)
		return
	}

	// A form post without JavaScript wants a page back; the board's own
	// requests only want to know it worked.
	if r.Header.Get("Accept") == "application/json" || r.FormValue("tyst") == "1" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, s.backTo(r, "/kandidater"), http.StatusSeeOther)
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

// handleDeleteCandidate takes one card off the board.
//
// No approval queue, unlike removing a member. A card is a working note about
// somebody the association has not agreed anything with yet, and the team that
// wrote it down is the team that should be able to throw it away — a duplicate
// from someone pressing the button twice, or a row typed in by mistake, is
// theirs to tidy. What it does not do is touch the register: somebody who was
// welcomed is a member, and stays one with their card gone.
func (s *Server) handleDeleteCandidate(w http.ResponseWriter, r *http.Request, v *view) {
	c, ok := s.candidate(w, r, v)
	if !ok {
		return
	}
	if err := s.store.DeleteCandidate(r.Context(), c.ID); err != nil {
		s.log.Error("could not delete a candidate", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	s.auditCandidate(r.Context(), v, "candidate.deleted", c, stageName(s.cfg, c, v))
	s.log.Info("a candidate card was deleted",
		"id", c.ID, "name", c.Name(), "stage", c.Stage, "by", v.Session.Email)
	s.flash(w, "ok", "flash.candidate.deleted", c.Name())
	http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
}

// handleClearStage empties one finished column.
//
// Only a closed stage may be cleared, and that is checked here rather than
// only hidden in the page. Welcomed and declined are history: the cards have
// done their work and the column grows forever otherwise. An open stage is
// the opposite — it holds people who are waiting to hear from us, and
// somebody who sent the form and heard nothing is the worst thing this
// register can do to a person. A hand-made request asking to empty "Nya" is
// refused rather than obeyed.
func (s *Server) handleClearStage(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	id := strings.TrimSpace(r.FormValue("steg"))
	st, known := s.cfg.Pipeline.Stage(id)
	if !known {
		s.flash(w, "error", "flash.candidate.nostage")
		http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
		return
	}
	if !st.Closed {
		s.flash(w, "error", "flash.candidate.stillopen", st.NameFor(string(v.Lang)))
		http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
		return
	}

	gone, err := s.store.ClearCandidateStage(r.Context(), st.ID, v.Loc)
	if err != nil {
		s.log.Error("could not clear a stage", "stage", st.ID, "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	name := st.NameFor(string(v.Lang))
	if len(gone) == 0 {
		s.flash(w, "warn", "flash.candidate.cleared.none", name)
		http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
		return
	}

	// One line per card rather than one line saying "12 cards". A month later
	// the only question anybody asks of this trail is who was on it.
	for _, c := range gone {
		s.auditCandidate(r.Context(), v, "candidate.deleted", c, name)
	}
	s.log.Info("a finished column was cleared",
		"stage", st.ID, "cards", len(gone), "by", v.Session.Email)
	s.flash(w, "ok", "flash.candidate.cleared", strconv.Itoa(len(gone)), name)
	http.Redirect(w, r, "/kandidater", http.StatusSeeOther)
}

// auditCandidate writes a deleted card into the trail.
//
// The member trail is the right place for it even though a candidate is not a
// member: it is the one page the board already reads to find out what happened
// to the register, and a card that existed and now does not belongs there. The
// row carries the name and address as text, and no member id unless the card
// had reached one, so the log renders it as a plain line rather than a link to
// somebody who may never have existed in the register.
func (s *Server) auditCandidate(ctx context.Context, v *view, action string,
	c store.Candidate, detail string) {

	subject := c.Name()
	if c.Email != "" {
		subject += " <" + c.Email + ">"
	}
	err := s.store.Log(ctx, store.Entry{
		At: s.now(), Actor: v.Session.Email, Role: string(v.Role), Action: action,
		MemberID: c.MemberID, Subject: subject, Detail: detail,
	})
	if err != nil {
		s.log.Error("could not write the audit trail", "action", action, "err", err)
	}
}

// stageName is the column a card was standing in, for the trail.
func stageName(cfg *config.Config, c store.Candidate, v *view) string {
	if st, known := cfg.Pipeline.Stage(c.Stage); known {
		return st.NameFor(string(v.Lang))
	}
	return c.Stage
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
