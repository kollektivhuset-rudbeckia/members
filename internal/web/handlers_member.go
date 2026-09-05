package web

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

// emailShape is a deliberately loose check. Its job is to catch a missing @
// or a stray space before the address reaches Google, not to adjudicate
// RFC 5321 — the real verdict comes from the group sync, and the sync page
// reports it in the member's own name.
var emailShape = regexp.MustCompile(`^[^@\s]+@[^@\s.]+(\.[^@\s.]+)+$`)

// memberForm is the add and edit form, kept as one type so a rejected
// submission can be handed straight back with everything still filled in.
type memberForm struct {
	FirstName string
	LastName  string
	Email     string
	Phone     string
	Kind      string
	AlsoIn    []string
	Apartment string
	JoinedOn  string
	LeftOn    string
	Note      string
	// Reason is what the board will read when this becomes a proposal.
	Reason string
	// Errors are catalogue keys, one per field that will not do.
	Errors map[string]string
}

// Bad reports whether anything is wrong with the form.
func (f memberForm) Bad() bool { return len(f.Errors) > 0 }

// Err is what a template asks for a field's complaint.
func (f memberForm) Err(field string) string { return f.Errors[field] }

// AlsoInKind reports whether an extra group is ticked, for redrawing a form
// that came back rejected.
func (f memberForm) AlsoInKind(k config.Kind) bool {
	for _, v := range f.AlsoIn {
		if v == string(k) {
			return true
		}
	}
	return false
}

func readMemberForm(r *http.Request) memberForm {
	return memberForm{
		FirstName: strings.TrimSpace(r.FormValue("fornamn")),
		LastName:  strings.TrimSpace(r.FormValue("efternamn")),
		Email:     store.Email(r.FormValue("epost")),
		Phone:     strings.TrimSpace(r.FormValue("telefon")),
		Kind:      strings.TrimSpace(r.FormValue("typ")),
		AlsoIn:    r.Form["ocksa"],
		Apartment: strings.TrimSpace(r.FormValue("lagenhet")),
		JoinedOn:  strings.TrimSpace(r.FormValue("medlem_sedan")),
		LeftOn:    strings.TrimSpace(r.FormValue("uttradd")),
		Note:      strings.TrimSpace(r.FormValue("anteckning")),
		Reason:    strings.TrimSpace(r.FormValue("anledning")),
	}
}

func formOf(m store.Member, loc *time.Location) memberForm {
	f := memberForm{
		FirstName: m.FirstName, LastName: m.LastName, Email: m.Email, Phone: m.Phone,
		Kind: string(m.Kind), Apartment: m.Apartment, Note: m.Note,
		JoinedOn: i18n.ISODate(m.JoinedOn.In(loc)),
		AlsoIn:   kindStrings(m.AlsoIn),
	}
	if m.LeftOn.Valid {
		f.LeftOn = i18n.ISODate(m.LeftOn.Time.In(loc))
	}
	return f
}

// validate turns the form into a member, or into a set of complaints.
func (f *memberForm) validate(loc *time.Location, now time.Time) (store.Member, bool) {
	f.Errors = map[string]string{}
	var m store.Member

	if f.FirstName == "" && f.LastName == "" {
		f.Errors["name"] = "form.err.name"
	}
	if f.Email == "" {
		f.Errors["email"] = "form.err.email.missing"
	} else if !emailShape.MatchString(f.Email) {
		f.Errors["email"] = "form.err.email.shape"
	}
	kind, ok := config.ParseKind(f.Kind)
	if !ok {
		f.Errors["kind"] = "form.err.kind"
	}
	// A member is always in their own kind's group; ticking it as an extra
	// would be a second, contradictory way to say the same thing.
	var alsoIn []config.Kind
	for _, raw := range f.AlsoIn {
		extra, ok := config.ParseKind(raw)
		if !ok {
			f.Errors["alsoin"] = "form.err.kind"
			continue
		}
		if extra != kind {
			alsoIn = append(alsoIn, extra)
		}
	}

	joined, err := store.ParseDay(f.JoinedOn, loc)
	switch {
	case f.JoinedOn == "":
		f.Errors["joined"] = "form.err.joined.missing"
	case err != nil:
		f.Errors["joined"] = "form.err.date"
	case joined.After(now.AddDate(0, 0, 1)):
		// Tomorrow is allowed — somebody writing down a member late in the
		// evening in another timezone should not be argued with — but next
		// year is a typo, and it would quietly break the tenure column.
		f.Errors["joined"] = "form.err.joined.future"
	}

	var left sql.NullTime
	if f.LeftOn != "" {
		t, err := store.ParseDay(f.LeftOn, loc)
		switch {
		case err != nil:
			f.Errors["left"] = "form.err.date"
		case t.Before(joined):
			f.Errors["left"] = "form.err.left.before"
		default:
			left = sql.NullTime{Time: t, Valid: true}
		}
	}
	if len(f.Errors) > 0 {
		return m, false
	}
	return store.Member{
		FirstName: f.FirstName, LastName: f.LastName, Email: f.Email, Phone: f.Phone,
		Kind: kind, AlsoIn: alsoIn, Apartment: f.Apartment,
		JoinedOn: joined, LeftOn: left, Note: f.Note,
	}, true
}

// --- adding ---------------------------------------------------------------

func (s *Server) handleNewForm(w http.ResponseWriter, r *http.Request, v *view) {
	kind := r.URL.Query().Get("typ")
	if _, ok := config.ParseKind(kind); !ok {
		kind = string(config.KindBo)
	}
	s.renderNew(w, r, v, memberForm{
		Kind:     kind,
		JoinedOn: i18n.ISODate(v.Now),
	}, http.StatusOK)
}

func (s *Server) renderNew(w http.ResponseWriter, r *http.Request, v *view, f memberForm, status int) {
	v.Title = i18n.T(v.Lang, "new.title")
	v.Data = map[string]any{"Form": f, "Kinds": s.kindOptions(v.Lang), "New": true}
	s.render(w, r, status, "new.html", v)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	f := readMemberForm(r)
	m, ok := f.validate(v.Loc, v.Now)
	if !ok {
		s.renderNew(w, r, v, f, http.StatusUnprocessableEntity)
		return
	}
	m.ID = auth.ID()
	m.CreatedAt, m.UpdatedAt = s.now(), s.now()
	m.CreatedBy, m.UpdatedBy = v.Session.Email, v.Session.Email

	if err := s.store.CreateMember(r.Context(), m); err != nil {
		if errors.Is(err, store.ErrDuplicateEmail) {
			f.Errors = map[string]string{"email": "form.err.email.taken"}
			s.renderNew(w, r, v, f, http.StatusConflict)
			return
		}
		s.log.Error("could not add a member", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}

	s.audit(r.Context(), v, "member.added", m, "")
	// Straight into the group: somebody who has shown interest should hear
	// from the house today, not once the money has landed.
	s.sync.Nudge(sync.TriggerMember)
	s.flash(w, "ok", "flash.added", m.Name())
	s.log.Info("member added", "id", m.ID, "email", m.Email, "by", v.Session.Email)
	http.Redirect(w, r, "/medlem/"+m.ID, http.StatusSeeOther)
}

// --- one member -----------------------------------------------------------

func (s *Server) handleMember(w http.ResponseWriter, r *http.Request, v *view) {
	m, ok := s.member(w, r, v)
	if !ok {
		return
	}
	ctx := r.Context()
	payments, err := s.store.PaymentsFor(ctx, m.ID, v.Loc)
	if err != nil {
		s.log.Error("could not read the payments", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}
	proposals, err := s.store.ProposalsFor(ctx, m.ID)
	if err != nil {
		s.log.Error("could not read the proposals", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}
	trail, err := s.store.AuditFor(ctx, m.ID, 25)
	if err != nil {
		s.log.Error("could not read the audit trail", "err", err)
	}
	states, err := s.store.SyncStates(ctx)
	if err != nil {
		s.log.Error("could not read the sync state", "err", err)
	}

	status := membership.Compute(m, payments, s.cfg, s.now())
	v.Title = m.Name()
	v.Data = map[string]any{
		"Member":     m,
		"Status":     status,
		"Form":       formOf(m, v.Loc),
		"Kinds":      s.kindOptions(v.Lang),
		"Payments":   payments,
		"Proposals":  proposals,
		"Pending":    firstPending(proposals),
		"Trail":      trail,
		"Sync":       syncFor(states, m.Email),
		"Years":      s.feeYears(status),
		"Group":      s.groupFor(m.Kind),
		"AlsoGroups": s.groupsFor(m.AlsoIn),
	}
	s.render(w, r, http.StatusOK, "member.html", v)
}

// firstPending is the proposal blocking further changes to this member, if
// any. A second proposal on top of an undecided one would leave the board
// deciding between two versions of a change nobody can reconstruct.
func firstPending(list []store.Proposal) *store.Proposal {
	for i := range list {
		if list[i].Open() {
			return &list[i]
		}
	}
	return nil
}

// syncFor picks out everything the synchroniser has to say about one address.
func syncFor(states []store.SyncState, email string) []store.SyncState {
	var out []store.SyncState
	for _, st := range states {
		if st.Address == store.Email(email) {
			out = append(out, st)
		}
	}
	return out
}

// feeYears are the years the payment table on a member's page shows: back to
// the year they joined, and never more than a screenful.
func (s *Server) feeYears(st membership.Status) []int {
	first := st.Member.JoinedOn.Year()
	if st.Year-first > 9 {
		first = st.Year - 9
	}
	out := make([]int, 0, st.Year-first+1)
	for y := st.Year; y >= first; y-- {
		out = append(out, y)
	}
	return out
}

// groupsFor is the addresses behind a set of extra memberships.
func (s *Server) groupsFor(kinds []config.Kind) []string {
	var out []string
	for _, k := range kinds {
		if address := s.groupFor(k); address != "" {
			out = append(out, address)
		}
	}
	return out
}

func (s *Server) groupFor(k config.Kind) string {
	if g, ok := s.cfg.GroupFor(k); ok {
		return g.Email
	}
	return ""
}

// member reads the member named in the path, rendering the not-found page if
// there is none.
func (s *Server) member(w http.ResponseWriter, r *http.Request, v *view) (store.Member, bool) {
	m, err := s.store.Member(r.Context(), r.PathValue("id"), v.Loc)
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound, "error.nomember", "error.nomember.how")
		return m, false
	}
	if err != nil {
		s.log.Error("could not read a member", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return m, false
	}
	return m, true
}

// --- changing -------------------------------------------------------------

// handleSave applies an edit, or files it as a proposal.
//
// Which of the two happens is decided here rather than in the template: the
// form can be wrong about what a button will do, but this cannot.
func (s *Server) handleSave(w http.ResponseWriter, r *http.Request, v *view) {
	if !v.May("edit") && !v.May("propose") {
		s.denied(w, r, v, config.PermEdit)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	existing, ok := s.member(w, r, v)
	if !ok {
		return
	}

	f := readMemberForm(r)
	updated, valid := f.validate(v.Loc, v.Now)
	if !valid {
		s.rerenderMember(w, r, v, existing, f, http.StatusUnprocessableEntity)
		return
	}
	updated.ID = existing.ID
	updated.CreatedAt, updated.CreatedBy = existing.CreatedAt, existing.CreatedBy
	updated.UpdatedAt, updated.UpdatedBy = s.now(), v.Session.Email

	if !v.May("edit") {
		s.propose(w, r, v, existing, store.ProposeUpdate, store.SnapshotOf(updated), f.Reason)
		return
	}

	if err := s.store.UpdateMember(r.Context(), updated); err != nil {
		if errors.Is(err, store.ErrDuplicateEmail) {
			f.Errors = map[string]string{"email": "form.err.email.taken"}
			s.rerenderMember(w, r, v, existing, f, http.StatusConflict)
			return
		}
		s.log.Error("could not save a member", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}

	// A pending proposal about this member was written against the version
	// that has just been replaced. Deciding it now would silently undo this
	// edit, so it is retired instead and whoever asked can ask again.
	if err := s.store.StaleProposalsFor(r.Context(), updated.ID, s.now()); err != nil {
		s.log.Warn("could not retire the proposals for a changed member", "err", err)
	}

	s.audit(r.Context(), v, "member.changed", updated, describeChange(existing, updated, v.Loc))
	s.sync.Nudge(sync.TriggerMember)
	s.flash(w, "ok", "flash.saved", updated.Name())
	s.log.Info("member changed", "id", updated.ID, "by", v.Session.Email)
	http.Redirect(w, r, "/medlem/"+updated.ID, http.StatusSeeOther)
}

// rerenderMember shows the member page again with a rejected form on it.
func (s *Server) rerenderMember(w http.ResponseWriter, r *http.Request, v *view,
	m store.Member, f memberForm, status int) {

	ctx := r.Context()
	payments, _ := s.store.PaymentsFor(ctx, m.ID, v.Loc)
	proposals, _ := s.store.ProposalsFor(ctx, m.ID)
	trail, _ := s.store.AuditFor(ctx, m.ID, 25)
	states, _ := s.store.SyncStates(ctx)
	st := membership.Compute(m, payments, s.cfg, s.now())

	v.Title = m.Name()
	v.Data = map[string]any{
		"Member": m, "Status": st, "Form": f, "Kinds": s.kindOptions(v.Lang),
		"Payments": payments, "Proposals": proposals, "Pending": firstPending(proposals),
		"Trail": trail, "Sync": syncFor(states, m.Email), "Years": s.feeYears(st),
		"Group": s.groupFor(m.Kind), "AlsoGroups": s.groupsFor(m.AlsoIn),
		"OpenEditor": true,
	}
	s.render(w, r, status, "member.html", v)
}

// handleDelete removes a member, or asks the board to.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request, v *view) {
	if !v.May("delete") && !v.May("propose") {
		s.denied(w, r, v, config.PermDelete)
		return
	}
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	m, ok := s.member(w, r, v)
	if !ok {
		return
	}
	reason := strings.TrimSpace(r.FormValue("anledning"))

	if !v.May("delete") {
		s.propose(w, r, v, m, store.ProposeDelete, store.SnapshotOf(m), reason)
		return
	}
	if err := s.removeMember(r.Context(), v, m, reason); err != nil {
		s.log.Error("could not remove a member", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	s.flash(w, "ok", "flash.removed", m.Name())
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// removeMember deletes a member and everything hanging off them, leaving the
// audit trail behind. It is shared by the board deleting outright and by the
// board approving somebody else's proposal to delete.
func (s *Server) removeMember(ctx context.Context, v *view, m store.Member, reason string) error {
	if err := s.store.StaleProposalsFor(ctx, m.ID, s.now()); err != nil {
		s.log.Warn("could not retire the proposals for a removed member", "err", err)
	}
	if err := s.store.DeleteMember(ctx, m.ID); err != nil {
		return err
	}
	s.audit(ctx, v, "member.removed", m, reason)
	// The address has to come out of the groups, and it is no longer in the
	// register to be worked out from — so this nudge matters more than most.
	s.sync.Nudge(sync.TriggerMember)
	s.log.Info("member removed", "id", m.ID, "email", m.Email, "by", v.Session.Email)
	return nil
}

// propose files a change for the board and sends the proposer back to the
// member with a note saying what happens next.
func (s *Server) propose(w http.ResponseWriter, r *http.Request, v *view,
	m store.Member, kind store.ProposalKind, after store.Snapshot, reason string) {

	pending, err := s.store.ProposalsFor(r.Context(), m.ID)
	if err == nil && firstPending(pending) != nil {
		s.flash(w, "warn", "flash.alreadypending")
		http.Redirect(w, r, "/medlem/"+m.ID, http.StatusSeeOther)
		return
	}

	p := store.Proposal{
		ID: auth.ID(), MemberID: m.ID, Kind: kind,
		Before: store.SnapshotOf(m), After: after, Reason: reason,
		Status: store.Pending, ProposedBy: v.Session.Email, ProposedAt: s.now(),
	}
	if err := s.store.CreateProposal(r.Context(), p); err != nil {
		s.log.Error("could not file a proposal", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	action := "proposal.update"
	if kind == store.ProposeDelete {
		action = "proposal.delete"
	}
	s.audit(r.Context(), v, action, m, reason)
	s.flash(w, "ok", "flash.proposed", s.rt.AccountFor(config.RoleBoard))
	s.log.Info("proposal filed", "id", p.ID, "member", m.ID, "kind", kind, "by", v.Session.Email)
	http.Redirect(w, r, "/medlem/"+m.ID, http.StatusSeeOther)
}

// audit appends to the trail, keeping the member's name and address as they
// were so the line still reads after the row is gone.
func (s *Server) audit(ctx context.Context, v *view, action string, m store.Member, detail string) {
	err := s.store.Log(ctx, store.Entry{
		At: s.now(), Actor: v.Session.Email, Role: string(v.Role), Action: action,
		MemberID: m.ID, Subject: m.Name() + " <" + m.Email + ">", Detail: detail,
	})
	if err != nil {
		// The trail failing must not fail the thing it was recording, but it
		// is not nothing either: say so loudly in the log.
		s.log.Error("could not write the audit trail", "action", action, "err", err)
	}
}

// describeChange summarises an edit in one line for the audit trail, so the
// board can read what happened without diffing two snapshots by eye.
func describeChange(before, after store.Member, loc *time.Location) string {
	var parts []string
	add := func(field, was, now string) {
		if was != now {
			parts = append(parts, field+": "+quote(was)+" → "+quote(now))
		}
	}
	add("namn", before.Name(), after.Name())
	add("e-post", before.Email, after.Email)
	add("telefon", before.Phone, after.Phone)
	add("typ", string(before.Kind), string(after.Kind))
	add("även med i", strings.Join(kindStrings(before.AlsoIn), " "),
		strings.Join(kindStrings(after.AlsoIn), " "))
	add("lägenhet", before.Apartment, after.Apartment)
	add("medlem sedan", i18n.ISODate(before.JoinedOn.In(loc)), i18n.ISODate(after.JoinedOn.In(loc)))
	add("utträdd", leftLabel(before, loc), leftLabel(after, loc))
	add("anteckning", before.Note, after.Note)
	return strings.Join(parts, "; ")
}

// kindStrings renders a set of memberships for a form or a log line.
func kindStrings(kinds []config.Kind) []string {
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		out = append(out, string(k))
	}
	return out
}

func leftLabel(m store.Member, loc *time.Location) string {
	if !m.LeftOn.Valid {
		return ""
	}
	return i18n.ISODate(m.LeftOn.Time.In(loc))
}

func quote(s string) string {
	if s == "" {
		return "–"
	}
	return "”" + s + "”"
}
