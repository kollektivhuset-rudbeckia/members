package web

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

// targetView groups everything known about one place the register keeps in
// step: whether it is working, what is wrong with it, and what the last few
// passes over it did.
type targetView struct {
	Target string
	Kind   string
	Name   string
	// OK is the target as a whole. An address failing inside a working target
	// is a different, smaller problem, and the page distinguishes them.
	OK      bool
	Message string
	Trouble []sync.Trouble
	Runs    []store.SyncRun
	Members int
}

// Loud reports whether anything here has been wrong long enough to matter.
func (t targetView) Loud() bool {
	if !t.OK {
		return true
	}
	for _, x := range t.Trouble {
		if x.Loud {
			return true
		}
	}
	return false
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()

	trouble, err := s.sync.Trouble(ctx, s.store)
	if err != nil {
		s.log.Error("could not read the sync state", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}
	runs, err := s.store.SyncRuns(ctx, 120)
	if err != nil {
		s.log.Error("could not read the sync log", "err", err)
	}
	states, err := s.store.SyncStates(ctx)
	if err != nil {
		s.log.Error("could not read the sync state", "err", err)
	}

	byTarget := map[string]*targetView{}
	order := []string{}
	for _, target := range s.cfg.Targets() {
		tv := &targetView{Target: target, OK: true, Kind: kindOf(target), Name: subjectOf(target)}
		byTarget[target] = tv
		order = append(order, target)
	}
	// A target that has since been taken out of the configuration can still
	// have state in the database. Showing it is right — it says the group is
	// no longer being kept in step, which somebody might not have meant.
	for _, st := range states {
		if _, ok := byTarget[st.Target]; !ok {
			tv := &targetView{Target: st.Target, OK: true, Kind: kindOf(st.Target),
				Name: subjectOf(st.Target)}
			byTarget[st.Target] = tv
			order = append(order, st.Target)
		}
		if st.WholeTarget() {
			byTarget[st.Target].OK = st.OK
			byTarget[st.Target].Message = st.Message
		} else if st.OK && st.Intent == store.Present {
			byTarget[st.Target].Members++
		}
	}
	for _, t := range trouble {
		if tv, ok := byTarget[t.State.Target]; ok && !t.State.WholeTarget() {
			tv.Trouble = append(tv.Trouble, t)
		}
	}
	for _, run := range runs {
		if tv, ok := byTarget[run.Target]; ok && len(tv.Runs) < 8 {
			tv.Runs = append(tv.Runs, run)
		}
	}

	targets := make([]targetView, 0, len(order))
	for _, name := range order {
		targets = append(targets, *byTarget[name])
	}
	// Broken things first: the page is read when something is wrong.
	sort.SliceStable(targets, func(i, j int) bool {
		if targets[i].Loud() != targets[j].Loud() {
			return targets[i].Loud()
		}
		return false
	})

	v.Title = i18n.T(v.Lang, "sync.title")
	v.Data = map[string]any{
		"Targets":  targets,
		"Trouble":  trouble,
		"Runs":     runs,
		"Last":     s.sync.Last(),
		"Progress": s.sync.Progress(),
		"On":       s.sync.Enabled(),
		"Every":    s.cfg.Sync.Interval(),
		"SA":       serviceAccountAddress(s),
		"Admin":    s.rt.Google.AdminSubject,
		"SheetID":  s.cfg.Sheet.ID,
		"SheetTab": s.cfg.Sheet.TabName(),
	}
	s.render(w, r, http.StatusOK, "sync.html", v)
}

func serviceAccountAddress(s *Server) string {
	if s.rt.Google.ServiceAccount == nil {
		return ""
	}
	return s.rt.Google.ServiceAccount.ClientEmail
}

func kindOf(target string) string {
	if i := strings.IndexByte(target, ':'); i > 0 {
		return target[:i]
	}
	return target
}

func subjectOf(target string) string {
	if i := strings.IndexByte(target, ':'); i > 0 {
		return target[i+1:]
	}
	return target
}

// handleSyncNow starts a reconciliation and returns straight away.
//
// It used to run the pass inside the request and hand back the result, which
// reads better on paper and does not survive contact with a first run: three
// address books is several hundred calls to Google and minutes of waiting,
// and the request times out long before. So the page says it has started and
// then shows it happening.
//
// A `mal` parameter runs one target on its own, which is what you want when
// one account is misbehaving and the other five are fine.
func (s *Server) handleSyncNow(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	if !s.sync.Enabled() {
		s.flash(w, "warn", "flash.syncoff")
		http.Redirect(w, r, "/synk", http.StatusSeeOther)
		return
	}

	only := strings.TrimSpace(r.FormValue("mal"))
	if only != "" && !s.knownTarget(only) {
		s.flash(w, "error", "flash.notarget")
		http.Redirect(w, r, "/synk", http.StatusSeeOther)
		return
	}

	switch err := s.sync.Start(sync.TriggerManual, only); {
	case errors.Is(err, sync.ErrBusy):
		s.flash(w, "warn", "flash.syncbusy")
	case err != nil:
		s.log.Error("could not start a synchronisation", "err", err)
		s.flash(w, "error", "flash.syncfailed")
	case only != "":
		s.flash(w, "ok", "flash.syncstarted.one", subjectOf(only))
		s.log.Info("synchronisation of one target started by hand",
			"by", v.Session.Email, "target", only)
	default:
		s.flash(w, "ok", "flash.syncstarted")
		s.log.Info("synchronisation started by hand", "by", v.Session.Email)
	}
	http.Redirect(w, r, "/synk", http.StatusSeeOther)
}

// knownTarget guards the parameter against anything not in the configuration,
// so a hand-typed address cannot make the reconciler chase something that is
// not one of ours.
func (s *Server) knownTarget(target string) bool {
	for _, t := range s.cfg.Targets() {
		if t == target {
			return true
		}
	}
	return false
}

// handleAuditLog is the board's record of everything anybody has done.
func (s *Server) handleAuditLog(w http.ResponseWriter, r *http.Request, v *view) {
	entries, err := s.store.Audit(r.Context(), 300)
	if err != nil {
		s.log.Error("could not read the audit trail", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}
	v.Title = i18n.T(v.Lang, "log.title")
	v.Data = map[string]any{"Entries": entries}
	s.render(w, r, http.StatusOK, "log.html", v)
}
