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

	// No brake here, and nothing to brake. Everything below takes a label off
	// a card; nothing deletes one. Unlabelling loses nothing and the next run
	// puts it back if the register says so, so a limit would only ever leave
	// labels wrong until somebody went and raised it.

	for _, card := range strays {
		email := card.PrimaryEmail()
		touched = append(touched, email)
		who := whoIs(card)

		// Whether they moved between the two labels or left the register
		// altogether, the treatment is the same and it is never deletion:
		// the label comes off and the card stays. Only the wording differs,
		// because "moved to the other list" and "no longer in the register"
		// are different things for somebody reading the sync page.
		m, known := byKey[s.cfg.Sync.MatchKey(email)]
		moved := known && m.Current() && m.Kind != kind

		if !s.cfg.Contacts.Pruning() && !moved {
			run.OK, run.Failed = false, run.Failed+1
			s.recordAddress(ctx, target, email, "", store.Absent, false,
				who+" has the registry's label but is not in the register, and "+
					"contacts.prune is off, so the label was left alone")
			continue
		}

		if err := s.gc.ModifyLabel(ctx, mailbox, label.ResourceName, nil,
			[]string{card.ResourceName}); err != nil {
			run.OK, run.Failed = false, run.Failed+1
			s.recordAddress(ctx, target, email, "", store.Absent, false, err.Error())
			continue
		}
		run.Updated++
		if moved {
			s.log.Info("moved a contact to the other label",
				"mailbox", mailbox, "from", label.Name, "address", email, "name", who)
		} else {
			s.log.Info("took the registry's label off a contact; the card was kept",
				"mailbox", mailbox, "label", label.Name, "address", email, "name", who)
		}
		s.recordAddress(ctx, target, email, "", store.Absent, true, "")
	}
	return touched
}

// whoIs names a contact card for a person reading the sync page. A card with
// no name at all falls back to saying so rather than to an empty string,
// which would read as a missing word rather than a missing name.
func whoIs(p google.Person) string {
	if name := strings.TrimSpace(p.DisplayName()); name != "" {
		return name
	}
	if phone := strings.TrimSpace(p.PrimaryPhone()); phone != "" {
		return "a card with no name (" + phone + ")"
	}
	return "a card with no name"
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

// noteMark ends every note the registry writes, so that anybody looking at a
// contact card can see where the line came from. A fixed string rather than
// the configured site title, so that renaming the register does not leave two
// generations of wording in the same address book.
const noteMark = "· ur medlemsregistret"

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
	parts = append(parts, noteMark)
	return strings.Join(parts, " ")
}

// ours reports whether the registry wrote this card, rather than finding it
// already in somebody's address book.
//
// It matters because the two deserve opposite treatment. A card the registry
// created for a member who has since left is its own litter, and deleting it
// is tidying up. A card that was in the mailbox before the registry ever ran
// is somebody's own contact — a name and a number that may exist nowhere
// else — and the most the registry may do with it is take its label back off.
func ours(p google.Person) bool {
	return strings.Contains(p.Notes(), noteMark)
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
