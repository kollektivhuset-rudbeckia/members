// Package importer fills an empty register from what the association already
// has: the contact labels on styrelsen@rudbeckia.nu that an Apps Script has
// been copying into the Google groups until now.
//
// It is deliberately one-way and deliberately cautious. It never changes a
// member who is already in the register, never writes anything to Google, and
// reports everything it could not make sense of by name — because the
// interesting output of a migration is not the rows that worked.
package importer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/google"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// Candidate is one person the importer found, as it understood them.
type Candidate struct {
	Kind      config.Kind
	FirstName string
	LastName  string
	Email     string
	Phone     string
	Note      string
	// Source says where they came from, for the report.
	Source string
	// Problem says why this one cannot be imported. Empty means it can.
	Problem string
	// Existing is set when the register already holds this address.
	Existing bool
}

// Importable reports whether this candidate would be written.
func (c Candidate) Importable() bool { return c.Problem == "" && !c.Existing }

// Name is the candidate's name as it will be stored.
func (c Candidate) Name() string {
	return strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
}

// Result is what an import found and, if it was not a rehearsal, what it did.
type Result struct {
	Candidates []Candidate
	// Imported is how many members were written. It stays zero for a dry run.
	Imported int
	DryRun   bool
	// StrayInGroups are addresses that are in a Google group but under no
	// contact label, so importing the labels alone would lose them. They are
	// reported rather than imported: the register wants a name and a person,
	// and all a group has is an address.
	StrayInGroups []string
}

// Ready is the candidates that would be written.
func (r Result) Ready() []Candidate { return filter(r.Candidates, Candidate.Importable) }

// Problems is the candidates that need a human.
func (r Result) Problems() []Candidate {
	return filter(r.Candidates, func(c Candidate) bool { return c.Problem != "" })
}

// Skipped is the candidates already in the register.
func (r Result) Skipped() []Candidate {
	return filter(r.Candidates, func(c Candidate) bool { return c.Existing && c.Problem == "" })
}

func filter(list []Candidate, keep func(Candidate) bool) []Candidate {
	var out []Candidate
	for _, c := range list {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// Options is where to read from and what to assume about what is found.
type Options struct {
	// Mailbox is the address book to read: styrelsen@rudbeckia.nu, whose
	// labels are what the association has been maintaining by hand.
	Mailbox string
	// Labels maps a contact label to the membership it means. Empty falls
	// back to the labels in config.yaml.
	Labels map[string]config.Kind
	// JoinedOn is what to record as the day an imported member joined.
	//
	// A contact card does not say when somebody joined and there is nothing
	// to infer it from. Guessing would put a wrong number in the one column
	// the register exists to provide, so this is an explicit decision:
	// typically the association's founding date, or the day of the migration
	// with a note saying as much.
	JoinedOn time.Time
	// Note is written on every imported member, so that a tenure which is
	// really "unknown, imported" says so on the page.
	Note string
	// DryRun reads and reports without writing anything.
	DryRun bool
	// Actor is recorded as having added the members.
	Actor string
	// AlsoCheckGroups lists the Google groups too and reports any address in
	// them that no label accounts for.
	AlsoCheckGroups bool
}

// Run reads the address book and, unless this is a rehearsal, writes what it
// found into the register.
func Run(ctx context.Context, gc *google.Client, st *store.Store, cfg *config.Config,
	rt config.Runtime, opts Options) (Result, error) {

	result := Result{DryRun: opts.DryRun}
	loc := cfg.Location()

	labels := opts.Labels
	if len(labels) == 0 {
		labels = map[string]config.Kind{}
		for _, kind := range config.Kinds {
			labels[cfg.Contacts.LabelFor(kind)] = kind
		}
	}

	// The register as it stands, keyed for comparison so a differently-dotted
	// Gmail address is recognised as somebody we already have.
	existing := map[string]bool{}
	members, err := st.Members(ctx, loc)
	if err != nil {
		return result, fmt.Errorf("read the register: %w", err)
	}
	for _, m := range members {
		existing[cfg.Sync.MatchKey(m.Email)] = true
	}

	// Within one import, too: the same person is often on both labels, and
	// the second sighting must not become a second member.
	seen := map[string]bool{}

	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, label := range names {
		kind := labels[label]
		group, err := gc.EnsureLabel(ctx, opts.Mailbox, label)
		if err != nil {
			return result, fmt.Errorf("find the label %q in %s: %w", label, opts.Mailbox, err)
		}
		cards, err := gc.LabelMembers(ctx, opts.Mailbox, group.ResourceName)
		if err != nil {
			return result, fmt.Errorf("read the label %q in %s: %w", label, opts.Mailbox, err)
		}
		for _, card := range cards {
			c := candidateOf(card, kind, group.Name)
			c.Note = opts.Note
			key := cfg.Sync.MatchKey(c.Email)
			switch {
			case c.Problem != "":
			case existing[key]:
				c.Existing = true
			case seen[key]:
				c.Problem = "finns redan på en annan etikett i " + opts.Mailbox
			default:
				seen[key] = true
			}
			result.Candidates = append(result.Candidates, c)
		}
	}

	sort.SliceStable(result.Candidates, func(i, j int) bool {
		a, b := result.Candidates[i], result.Candidates[j]
		if !strings.EqualFold(a.LastName, b.LastName) {
			return strings.ToLower(a.LastName) < strings.ToLower(b.LastName)
		}
		return strings.ToLower(a.FirstName) < strings.ToLower(b.FirstName)
	})

	if opts.AlsoCheckGroups {
		strays, err := groupStrays(ctx, gc, cfg, rt, result.Candidates, existing)
		if err != nil {
			return result, err
		}
		result.StrayInGroups = strays
	}

	if opts.DryRun {
		return result, nil
	}

	joined := opts.JoinedOn
	if joined.IsZero() {
		joined = time.Now().In(loc)
	}
	now := time.Now()
	for _, c := range result.Candidates {
		if !c.Importable() {
			continue
		}
		m := store.Member{
			ID: auth.ID(), FirstName: c.FirstName, LastName: c.LastName,
			Email: c.Email, Phone: c.Phone, Kind: c.Kind, Note: c.Note,
			JoinedOn:  joined,
			CreatedAt: now, CreatedBy: opts.Actor,
			UpdatedAt: now, UpdatedBy: opts.Actor,
		}
		if err := st.CreateMember(ctx, m); err != nil {
			return result, fmt.Errorf("write %s: %w", c.Email, err)
		}
		result.Imported++
		if err := st.Log(ctx, store.Entry{
			At: now, Actor: opts.Actor, Role: string(config.RoleBoard),
			Action: "member.imported", MemberID: m.ID,
			Subject: m.Name() + " <" + m.Email + ">",
			Detail:  "från " + c.Source,
		}); err != nil {
			return result, fmt.Errorf("write the audit trail: %w", err)
		}
	}
	return result, nil
}

// candidateOf reads a contact card as a would-be member.
func candidateOf(p google.Person, kind config.Kind, label string) Candidate {
	c := Candidate{Kind: kind, Source: label + " i Google Kontakter"}
	c.Email = store.Email(p.PrimaryEmail())
	c.Phone = strings.TrimSpace(p.PrimaryPhone())

	for _, n := range p.Names {
		if n.GivenName != "" || n.FamilyName != "" {
			c.FirstName = strings.TrimSpace(n.GivenName)
			c.LastName = strings.TrimSpace(n.FamilyName)
			break
		}
	}
	if c.FirstName == "" && c.LastName == "" {
		// Some cards carry only a display name. Splitting on the last space
		// is right far more often than it is wrong for a Swedish name, and a
		// wrong split is visible and fixable on the member's own page.
		if display := strings.TrimSpace(p.DisplayName()); display != "" {
			if i := strings.LastIndexByte(display, ' '); i > 0 {
				c.FirstName, c.LastName = display[:i], display[i+1:]
			} else {
				c.FirstName = display
			}
		}
	}

	switch {
	case c.Email == "":
		c.Problem = "kontakten har ingen e-postadress"
	case !strings.Contains(c.Email, "@"):
		c.Problem = "e-postadressen ser inte ut som en adress: " + c.Email
	case c.FirstName == "" && c.LastName == "":
		c.Problem = "kontakten har inget namn"
	}
	return c
}

// groupStrays lists addresses that are in a Google group but under no label,
// which is how the migration finds the people the Apps Script kept by hand —
// the matlagsledare, and anybody else added straight to the group over the
// years. They are the whole reason the import reports rather than just runs:
// pruning would throw every one of them out on the first pass.
func groupStrays(ctx context.Context, gc *google.Client, cfg *config.Config, rt config.Runtime,
	found []Candidate, existing map[string]bool) ([]string, error) {

	covered := map[string]bool{}
	for _, c := range found {
		covered[cfg.Sync.MatchKey(c.Email)] = true
	}
	for key := range existing {
		covered[key] = true
	}

	var out []string
	for _, g := range cfg.Groups {
		have, err := gc.GroupMembers(ctx, rt.Google.AdminSubject, g.Email)
		if err != nil {
			return nil, fmt.Errorf("read the group %s: %w", g.Email, err)
		}
		for _, m := range have {
			key := cfg.Sync.MatchKey(m.Email)
			if covered[key] || g.Kept(m.Email) {
				continue
			}
			if _, isAccount := rt.Accounts[store.Email(m.Email)]; isAccount {
				continue
			}
			if role := strings.ToUpper(m.Role); role != "" && role != "MEMBER" {
				continue
			}
			out = append(out, m.Email+" ("+g.Email+")")
			covered[key] = true
		}
	}
	sort.Strings(out)
	return out, nil
}
