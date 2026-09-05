package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

// Change is one field a proposal would alter, ready for the board to read.
// The comparison happens here rather than in the template, so that a proposal
// which changes nothing at all is visibly a proposal that changes nothing.
type Change struct {
	FieldKey string
	Before   string
	After    string
}

// diff is what a proposal would do, field by field.
func diff(p store.Proposal) []Change {
	pairs := []struct {
		key         string
		before, now string
	}{
		{"form.firstname", p.Before.FirstName, p.After.FirstName},
		{"form.lastname", p.Before.LastName, p.After.LastName},
		{"form.email", p.Before.Email, p.After.Email},
		{"form.phone", p.Before.Phone, p.After.Phone},
		{"form.kind", string(p.Before.Kind), string(p.After.Kind)},
		{"form.apartment", p.Before.Apartment, p.After.Apartment},
		{"form.joined", p.Before.JoinedOn, p.After.JoinedOn},
		{"form.left", p.Before.LeftOn, p.After.LeftOn},
		{"form.note", p.Before.Note, p.After.Note},
	}
	var out []Change
	for _, pair := range pairs {
		if strings.TrimSpace(pair.before) != strings.TrimSpace(pair.now) {
			out = append(out, Change{FieldKey: pair.key, Before: pair.before, After: pair.now})
		}
	}
	return out
}

// proposalView is a proposal with everything a page needs beside it.
type proposalView struct {
	Proposal store.Proposal
	Changes  []Change
	// Member is the member as they stand now. A proposal about somebody who
	// has since been removed has none, which is exactly when the board needs
	// to be told rather than shown a link to nowhere.
	Member  store.Member
	Missing bool
	// Mine reports whether the signed-in account is the one who proposed it,
	// which is who may take it back.
	Mine bool
}

// Deletion reports whether this proposal asks for a removal.
func (p proposalView) Deletion() bool { return p.Proposal.Kind == store.ProposeDelete }

func (s *Server) handleProposals(w http.ResponseWriter, r *http.Request, v *view) {
	ctx := r.Context()
	pending, err := s.store.PendingProposals(ctx)
	if err != nil {
		s.log.Error("could not read the proposals", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}
	decided, err := s.store.DecidedProposals(ctx, 40)
	if err != nil {
		s.log.Error("could not read the decided proposals", "err", err)
	}

	v.Title = i18n.T(v.Lang, "proposals.title")
	v.Data = map[string]any{
		"Pending": s.decorate(ctx, v, pending),
		"Decided": s.decorate(ctx, v, decided),
		"Board":   s.rt.AccountFor("board"),
	}
	s.render(w, r, http.StatusOK, "proposals.html", v)
}

// decorate pairs each proposal with the member it concerns and the change it
// would make.
func (s *Server) decorate(ctx context.Context, v *view, list []store.Proposal) []proposalView {
	out := make([]proposalView, 0, len(list))
	for _, p := range list {
		pv := proposalView{
			Proposal: p,
			Changes:  diff(p),
			Mine:     strings.EqualFold(p.ProposedBy, v.Session.Email),
		}
		m, err := s.store.Member(ctx, p.MemberID, v.Loc)
		if err != nil {
			pv.Missing = true
		} else {
			pv.Member = m
		}
		out = append(out, pv)
	}
	return out
}

// handleApprove carries out a proposal.
//
// The proposal is applied to the member as they stand *now*, not to the
// snapshot taken when it was filed, and the two are compared first: if
// anybody has touched the member in between, approving would quietly undo
// their work. The board is told to look again instead.
func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	ctx := r.Context()
	p, ok := s.proposal(w, r, v)
	if !ok {
		return
	}
	if !p.Open() {
		s.flash(w, "warn", "flash.alreadydecided")
		http.Redirect(w, r, "/andringar", http.StatusSeeOther)
		return
	}

	m, err := s.store.Member(ctx, p.MemberID, v.Loc)
	if errors.Is(err, store.ErrNotFound) {
		// The member is already gone. A deletion has effectively happened; an
		// update has nothing left to apply to.
		if err := s.store.DecideProposal(ctx, p.ID, store.Stale, v.Session.Email,
			"medlemmen finns inte längre", s.now()); err != nil {
			s.log.Error("could not retire a proposal", "err", err)
		}
		s.flash(w, "warn", "flash.membergone")
		http.Redirect(w, r, "/andringar", http.StatusSeeOther)
		return
	}
	if err != nil {
		s.log.Error("could not read a member", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return
	}

	if store.SnapshotOf(m) != p.Before {
		if err := s.store.DecideProposal(ctx, p.ID, store.Stale, v.Session.Email,
			"medlemmen ändrades medan förslaget väntade", s.now()); err != nil {
			s.log.Error("could not retire a proposal", "err", err)
		}
		s.flash(w, "warn", "flash.movedon")
		http.Redirect(w, r, "/andringar", http.StatusSeeOther)
		return
	}

	note := strings.TrimSpace(r.FormValue("kommentar"))

	if p.Kind == store.ProposeDelete {
		if err := s.removeMember(ctx, v, m, "godkänt förslag från "+p.ProposedBy); err != nil {
			s.log.Error("could not remove a member", "err", err)
			s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
			return
		}
		if err := s.store.DecideProposal(ctx, p.ID, store.Approved, v.Session.Email, note, s.now()); err != nil {
			s.log.Error("could not record a decision", "err", err)
		}
		s.flash(w, "ok", "flash.approved.delete", m.Name())
		http.Redirect(w, r, "/andringar", http.StatusSeeOther)
		return
	}

	updated, err := p.After.Apply(m, v.Loc)
	if err != nil {
		s.log.Error("a proposal held an unreadable date", "proposal", p.ID, "err", err)
		s.flash(w, "error", "flash.badproposal")
		http.Redirect(w, r, "/andringar", http.StatusSeeOther)
		return
	}
	updated.UpdatedAt, updated.UpdatedBy = s.now(), v.Session.Email

	if err := s.store.UpdateMember(ctx, updated); err != nil {
		if errors.Is(err, store.ErrDuplicateEmail) {
			s.flash(w, "error", "flash.emailtaken", updated.Email)
			http.Redirect(w, r, "/andringar", http.StatusSeeOther)
			return
		}
		s.log.Error("could not apply a proposal", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	if err := s.store.DecideProposal(ctx, p.ID, store.Approved, v.Session.Email, note, s.now()); err != nil {
		s.log.Error("could not record a decision", "err", err)
	}

	s.audit(ctx, v, "proposal.approved", updated,
		"från "+p.ProposedBy+"; "+describeChange(m, updated, v.Loc))
	s.sync.Nudge(sync.TriggerProposal)
	s.flash(w, "ok", "flash.approved", updated.Name())
	s.log.Info("proposal approved", "id", p.ID, "member", updated.ID, "by", v.Session.Email)
	http.Redirect(w, r, "/andringar", http.StatusSeeOther)
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request, v *view) {
	if err := r.ParseForm(); err != nil {
		s.errorPage(w, r, http.StatusBadRequest, "error.form", "error.form.detail")
		return
	}
	p, ok := s.proposal(w, r, v)
	if !ok {
		return
	}
	note := strings.TrimSpace(r.FormValue("kommentar"))
	if err := s.store.DecideProposal(r.Context(), p.ID, store.Rejected, v.Session.Email, note, s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.flash(w, "warn", "flash.alreadydecided")
			http.Redirect(w, r, "/andringar", http.StatusSeeOther)
			return
		}
		s.log.Error("could not reject a proposal", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	if m, err := s.store.Member(r.Context(), p.MemberID, v.Loc); err == nil {
		s.audit(r.Context(), v, "proposal.rejected", m, "från "+p.ProposedBy+"; "+note)
	}
	s.flash(w, "warn", "flash.rejected")
	s.log.Info("proposal rejected", "id", p.ID, "by", v.Session.Email)
	http.Redirect(w, r, "/andringar", http.StatusSeeOther)
}

// handleWithdraw lets whoever filed a proposal take it back, which is easier
// for everybody than asking the board to reject their own second thoughts.
func (s *Server) handleWithdraw(w http.ResponseWriter, r *http.Request, v *view) {
	p, ok := s.proposal(w, r, v)
	if !ok {
		return
	}
	if !strings.EqualFold(p.ProposedBy, v.Session.Email) {
		s.renderError(w, r, http.StatusForbidden,
			i18n.T(v.Lang, "error.denied"), i18n.T(v.Lang, "error.notyours"))
		return
	}
	if err := s.store.DecideProposal(r.Context(), p.ID, store.Withdrawn, v.Session.Email, "", s.now()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.flash(w, "warn", "flash.alreadydecided")
			http.Redirect(w, r, "/andringar", http.StatusSeeOther)
			return
		}
		s.log.Error("could not withdraw a proposal", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.nowrite", "error.nowrite.how")
		return
	}
	s.flash(w, "ok", "flash.withdrawn")
	http.Redirect(w, r, safeNext(r.FormValue("tillbaka")), http.StatusSeeOther)
}

func (s *Server) proposal(w http.ResponseWriter, r *http.Request, v *view) (store.Proposal, bool) {
	p, err := s.store.Proposal(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		s.errorPage(w, r, http.StatusNotFound, "error.noproposal", "")
		return p, false
	}
	if err != nil {
		s.log.Error("could not read a proposal", "err", err)
		s.errorPage(w, r, http.StatusInternalServerError, "error.noread", "error.noread.how")
		return p, false
	}
	return p, true
}
