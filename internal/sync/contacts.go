package sync

import (
	"context"
	"fmt"
	"strings"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/google"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// syncContacts mirrors the register into one mailbox's address book.
//
// It only ever touches contacts inside the registry's own labels. The owner
// of the mailbox can keep whatever else they like in their contacts and the
// registry will never see it, let alone tidy it away.
func (s *Syncer) syncContacts(ctx context.Context, trigger Trigger, mailbox string,
	want map[config.Kind][]store.Member, byKey map[string]store.Member) store.SyncRun {

	target := "contacts:" + mailbox
	run := store.SyncRun{StartedAt: s.now(), Trigger: string(trigger), Target: target, OK: true}
	finish := func() store.SyncRun {
		run.FinishedAt = s.now()
		if !run.OK && run.Message == "" {
			run.Message = fmt.Sprintf("%d contacts could not be synchronised", run.Failed)
		}
		return run
	}

	// An empty register is not an instruction to empty the address books —
	// and here it would be worse than in a group, because a contact card
	// carries the name, the number and the notes, not just an address. On a
	// fresh deployment these labels are the only copy of the membership
	// there is, and they are what -import reads.
	total := 0
	for _, kind := range config.Kinds {
		for _, m := range want[kind] {
			if m.Kind == kind {
				total++
			}
		}
	}
	if total == 0 {
		run.OK = false
		run.Message = "the register is empty, so the address books were left alone — " +
			"fill the register first (see -import)"
		s.recordTarget(ctx, target, false, run.Message)
		s.log.Warn("refusing to touch an address book from an empty register", "mailbox", mailbox)
		return finish()
	}

	var touched []string
	for _, kind := range config.Kinds {
		label, err := s.gc.EnsureLabel(ctx, mailbox, s.cfg.Contacts.LabelFor(kind))
		if err != nil {
			run.OK, run.Failed = false, run.Failed+1
			run.Message = err.Error()
			s.recordTarget(ctx, target, false, err.Error())
			return finish()
		}
		cards, err := s.gc.LabelMembers(ctx, mailbox, label.ResourceName)
		if err != nil {
			run.OK, run.Failed = false, run.Failed+1
			run.Message = err.Error()
			s.recordTarget(ctx, target, false, err.Error())
			return finish()
		}
		// Only the member's own kind, not the groups they have merely been
		// added to. The extra membership exists so somebody can post to a
		// mailing list; duplicating their contact card under a second label
		// would say something about them that is not true.
		var own []store.Member
		for _, m := range want[kind] {
			if m.Kind == kind {
				own = append(own, m)
			}
		}
		touched = append(touched, s.reconcileLabel(ctx, target, mailbox, kind,
			label, cards, own, byKey, &run)...)
	}

	s.recordTarget(ctx, target, run.OK, run.Message)
	if err := s.store.ForgetSyncState(ctx, target, touched); err != nil {
		s.log.Warn("could not tidy the sync state", "target", target, "err", err)
	}
	return finish()
}

// reconcileLabel makes one label in one address book match one kind of
// membership, and returns the addresses it formed an opinion about.
func (s *Syncer) reconcileLabel(ctx context.Context, target, mailbox string, kind config.Kind,
	label google.ContactGroup, cards []google.Person, want []store.Member,
	byKey map[string]store.Member, run *store.SyncRun) []string {

	// Google allows the same address on several cards. Keeping the first and
	// treating the rest as strays is what makes the label converge instead of
	// growing a duplicate on every pass.
	have := make(map[string]google.Person, len(cards))
	var duplicates []google.Person
	for _, card := range cards {
		key := s.cfg.Sync.MatchKey(card.PrimaryEmail())
		if key == "" {
			// A card in our label with no address at all cannot be matched to
			// a member and cannot be mailed. It is a leftover, and it is
			// treated as one.
			duplicates = append(duplicates, card)
			continue
		}
		if _, seen := have[key]; seen {
			duplicates = append(duplicates, card)
			continue
		}
		have[key] = card
	}

	var touched []string
	wanted := map[string]bool{}

	for _, m := range want {
		email := store.Email(m.Email)
		key := s.cfg.Sync.MatchKey(m.Email)
		wanted[key] = true
		touched = append(touched, email)
		desired := s.card(m)

		card, exists := have[key]
		if !exists {
			if _, err := s.gc.CreateContact(ctx, mailbox, label.ResourceName, desired); err != nil {
				run.OK, run.Failed = false, run.Failed+1
				s.recordAddress(ctx, target, email, m.ID, store.Present, false, err.Error())
				continue
			}
			run.Added++
			s.recordAddress(ctx, target, email, m.ID, store.Present, true, "")
			continue
		}
		if !changed(card, desired) {
			s.recordAddress(ctx, target, email, m.ID, store.Present, true, "")
			continue
		}
		// Keep the card's identity and etag; replace only what we own.
		desired.ResourceName, desired.ETag = card.ResourceName, card.ETag
		if err := s.gc.UpdateContact(ctx, mailbox, desired); err != nil {
			run.OK, run.Failed = false, run.Failed+1
			s.recordAddress(ctx, target, email, m.ID, store.Present, false, err.Error())
			continue
		}
		run.Updated++
		s.recordAddress(ctx, target, email, m.ID, store.Present, true, "")
	}

	// Cards under our label that the register no longer puts there.
	var strays []google.Person
	for key, card := range have {
		if !wanted[key] {
			strays = append(strays, card)
		}
	}
	strays = append(strays, duplicates...)

	// The same brake as the groups. Deleting a card throws away a name and a
	// telephone number that may exist nowhere else, so the threshold counts
	// only the cards that would actually be deleted — somebody who has merely
	// moved between the two labels keeps their card and does not count.
	doomed := 0
	for _, card := range strays {
		if m, ok := byKey[s.cfg.Sync.MatchKey(card.PrimaryEmail())]; ok && m.Current() && m.Kind != kind {
			continue
		}
		doomed++
	}
	brake := s.cfg.Sync.MaxRemovalsPerRun
	held := brake > 0 && doomed > brake
	if held {
		s.log.Warn("refusing a bulk contact deletion", "mailbox", mailbox,
			"label", label.Name, "would_delete", doomed, "limit", brake)
	}

	for _, card := range strays {
		email := card.PrimaryEmail()
		touched = append(touched, email)

		// Somebody who moved between bomedlem and vänmedlem is not a stray:
		// they belong under the other label, and the pass for that label will
		// have picked them up. Take the label off and leave the card alone,
		// so their notes and history survive the move.
		if m, ok := byKey[s.cfg.Sync.MatchKey(email)]; ok && m.Current() && m.Kind != kind {
			if err := s.gc.ModifyLabel(ctx, mailbox, label.ResourceName, nil,
				[]string{card.ResourceName}); err != nil {
				run.OK, run.Failed = false, run.Failed+1
				s.recordAddress(ctx, target, email, m.ID, store.Absent, false, err.Error())
				continue
			}
			run.Updated++
			s.recordAddress(ctx, target, email, m.ID, store.Absent, true, "")
			continue
		}

		if held {
			run.OK, run.Failed = false, run.Failed+1
			s.recordAddress(ctx, target, email, "", store.Absent, false,
				fmt.Sprintf("would be deleted, but %d contacts at once is more than "+
					"max_removals_per_run (%d) — nothing was deleted", doomed, brake))
			continue
		}
		if !s.cfg.Contacts.Pruning() {
			run.OK, run.Failed = false, run.Failed+1
			s.recordAddress(ctx, target, email, "", store.Absent, false,
				"has the registry's label but is not in the register, and pruning is off")
			continue
		}
		if err := s.gc.DeleteContact(ctx, mailbox, card.ResourceName); err != nil {
			run.OK, run.Failed = false, run.Failed+1
			s.recordAddress(ctx, target, email, "", store.Absent, false, err.Error())
			continue
		}
		run.Removed++
		s.log.Info("removed a stray contact", "mailbox", mailbox, "address", email)
	}
	return touched
}

// card is the contact the registry wants to see for a member.
func (s *Syncer) card(m store.Member) google.Person {
	return google.Person{
		Names: []google.Name{{
			DisplayName: m.Name(),
			GivenName:   strings.TrimSpace(m.FirstName),
			FamilyName:  strings.TrimSpace(m.LastName),
		}},
		Emails:      []google.Email{{Value: store.Email(m.Email)}},
		Phones:      phones(m),
		Biographies: []google.Biography{{Value: s.note(m), ContentType: "TEXT_PLAIN"}},
	}
}

func phones(m store.Member) []google.Phone {
	if v := strings.TrimSpace(m.Phone); v != "" {
		return []google.Phone{{Value: v, Type: "mobile"}}
	}
	return nil
}

// note is the one line written into the contact's notes, so that looking
// somebody up on a phone answers the two questions anybody actually has:
// which sort of member is this, and how long have they been one.
func (s *Syncer) note(m store.Member) string {
	kind := "Vänmedlem"
	if m.Kind == config.KindBo {
		kind = "Bomedlem"
	}
	parts := []string{fmt.Sprintf("%s sedan %s", kind, m.JoinedOn.Format("2006-01-02"))}
	if apt := strings.TrimSpace(m.Apartment); apt != "" {
		parts = append(parts, "lgh "+apt)
	}
	parts = append(parts, "· ur "+s.cfg.Site.Title)
	return strings.Join(parts, " ")
}

// changed reports whether the card Google holds differs from the one the
// register wants, in the fields the register owns. Comparing rather than
// blindly writing keeps the pass quiet: an unchanged register makes no calls
// at all, which is what lets it run every ten minutes for years.
func changed(have, want google.Person) bool {
	if have.DisplayName() != want.Names[0].DisplayName {
		return true
	}
	if have.PrimaryEmail() != want.Emails[0].Value {
		return true
	}
	wantPhone := ""
	if len(want.Phones) > 0 {
		wantPhone = want.Phones[0].Value
	}
	if strings.TrimSpace(have.PrimaryPhone()) != wantPhone {
		return true
	}
	return strings.TrimSpace(have.Notes()) != strings.TrimSpace(want.Biographies[0].Value)
}
