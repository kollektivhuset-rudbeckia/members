// Package config loads the declarative description of the association — what
// the fee is, which Google groups and accounts the registry keeps in step —
// plus the runtime settings that come from the environment.
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Kind is the sort of membership someone holds. For the association the two
// are almost the same thing: they differ in which Google group the address
// belongs to, and in nothing else the registry cares about.
type Kind string

const (
	// KindBo is a bomedlem — someone who lives in the house.
	KindBo Kind = "bo"
	// KindVan is a vänmedlem — a friend of the house who does not live in it.
	KindVan Kind = "van"
)

// Kinds are the memberships on offer, in the order they are shown.
var Kinds = []Kind{KindBo, KindVan}

// Valid reports whether k is a membership the registry knows.
func (k Kind) Valid() bool { return k == KindBo || k == KindVan }

// ParseKind reads a membership from a form value or a database column.
func ParseKind(s string) (Kind, bool) {
	switch Kind(strings.ToLower(strings.TrimSpace(s))) {
	case KindBo:
		return KindBo, true
	case KindVan:
		return KindVan, true
	}
	return "", false
}

// Config is the whole association: how it presents itself, what it charges,
// and everything it keeps synchronised in Google Workspace.
type Config struct {
	Site       Site       `yaml:"site"`
	Membership Membership `yaml:"membership"`
	Groups     []Group    `yaml:"groups"`
	Contacts   Contacts   `yaml:"contacts"`
	Sheet      Sheet      `yaml:"sheet"`
	Chat       Chat       `yaml:"chat"`
	Pipeline   Pipeline   `yaml:"pipeline"`
	Sync       Sync       `yaml:"sync"`

	location *time.Location
}

// Site holds presentation-level settings shared by every page.
type Site struct {
	Title string `yaml:"title"`
	// Language is what a visitor sees before choosing for themselves. "sv" or "en".
	Language     string `yaml:"language"`
	Tagline      string `yaml:"tagline"`
	TaglineEN    string `yaml:"tagline_en"`
	HouseName    string `yaml:"house_name"`
	Timezone     string `yaml:"timezone"`
	HomeURL      string `yaml:"home_url"`
	SupportURL   string `yaml:"support_url"`
	FooterNote   string `yaml:"footer_note"`
	FooterNoteEN string `yaml:"footer_note_en"`
}

// TaglineFor and FooterFor give the house's own words in one language.
func (s Site) TaglineFor(lang string) string { return pick(lang, s.Tagline, s.TaglineEN) }
func (s Site) FooterFor(lang string) string  { return pick(lang, s.FooterNote, s.FooterNoteEN) }

// pick returns the wording for a language, falling back to the other one.
// Half a translation is better than a blank space where a sentence was.
func pick(lang, sv, en string) string {
	sv, en = strings.TrimSpace(sv), strings.TrimSpace(en)
	if lang == "en" {
		if en != "" {
			return en
		}
		return sv
	}
	if sv != "" {
		return sv
	}
	return en
}

// Membership is what the association charges and when it expects to be paid.
//
// The fee runs by calendar year: one payment covers January to December, and
// how long somebody has been a member is counted from the day they joined.
type Membership struct {
	// FeeKr is the yearly fee in whole kronor. Both kinds of member pay the
	// same unless FeeKrVan says otherwise.
	FeeKr    int `yaml:"fee_kr"`
	FeeKrVan int `yaml:"fee_kr_van"`
	// FeesByYear overrides the fee for particular years.
	//
	// The association raises the fee from time to time, and a register that
	// only knows this year's would quietly restate history: last year's
	// unpaid 250 would become an unpaid 300 the moment the meeting voted.
	// What somebody owes for a year is what the fee was that year.
	FeesByYear map[int]int `yaml:"fees_by_year"`
	// Bankgiro is the account the fee is paid into, shown to the cashiers so
	// they know which statement they are ticking off against, and to anybody
	// paying.
	Bankgiro string `yaml:"bankgiro"`
	// Swish is the association's Swish number. Empty offers bankgiro alone.
	Swish string `yaml:"swish"`
	// DueOn is the day of the year the fee is due, as "MM-DD".
	DueOn string `yaml:"due_on"`
	// GraceDays is how long after the due date an unpaid member is left in
	// peace before the registry starts calling them overdue.
	GraceDays int `yaml:"grace_days"`
	// NewMemberDays is how long somebody who has just joined gets, counted
	// from the day they were added. Somebody who shows interest in November
	// is not overdue the moment they are written down.
	NewMemberDays int `yaml:"new_member_days"`
	// PaymentReference is how the member is asked to label their transfer, so
	// the cashier can match it. %s is replaced with the member's name.
	PaymentReference string `yaml:"payment_reference"`
	// ChaseFromYear is the first year the register holds anybody to account
	// for. Left at zero it works itself out: this year and the last, never
	// reaching back past the day a member joined or the day their row was
	// created. Set it to a year to chase the whole history from there.
	ChaseFromYear int `yaml:"chase_from_year"`
}

// FeeFor returns the fee for a kind of membership in a given year, in kronor.
//
// A year with its own entry in fees_by_year wins outright, for both kinds:
// when the association votes a new fee it votes one number, not one per sort
// of member. Everything else falls back to the standing fee.
func (m Membership) FeeFor(k Kind, year int) int {
	if fee, ok := m.FeesByYear[year]; ok {
		return fee
	}
	if k == KindVan && m.FeeKrVan > 0 {
		return m.FeeKrVan
	}
	return m.FeeKr
}

// TakesSwish reports whether Swish is on offer.
func (m Membership) TakesSwish() bool { return strings.TrimSpace(m.Swish) != "" }

// SwishNumber is the number with the spaces people write it with taken out,
// which is the form the Swish payload wants.
func (m Membership) SwishNumber() string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, m.Swish)
}

// Reference is how somebody paying is asked to label the transfer, so the
// cashier can match it to a person.
func (m Membership) Reference(name string) string {
	pattern := strings.TrimSpace(m.PaymentReference)
	if pattern == "" {
		pattern = "Medlemsavgift %s"
	}
	if !strings.Contains(pattern, "%s") {
		return strings.TrimSpace(pattern + " " + name)
	}
	return strings.TrimSpace(fmt.Sprintf(pattern, name))
}

// FeeYears are the years with a fee of their own, oldest first, so a page can
// show what is coming without guessing.
func (m Membership) FeeYears() []int {
	out := make([]int, 0, len(m.FeesByYear))
	for y := range m.FeesByYear {
		out = append(out, y)
	}
	sort.Ints(out)
	return out
}

// DueDate returns the day the fee for a year is due.
func (m Membership) DueDate(year int, loc *time.Location) time.Time {
	month, day := 3, 31
	if _, err := fmt.Sscanf(m.DueOn, "%d-%d", &month, &day); err != nil || month < 1 || month > 12 {
		month, day = 3, 31
	}
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, loc)
}

// Group is one Google group the registry keeps in step with a membership.
type Group struct {
	// Kind says whose addresses belong in this group.
	Kind Kind `yaml:"kind"`
	// Email is the group address, e.g. "bomedlemmar@rudbeckia.nu".
	Email string `yaml:"email"`
	// Name is what the group is called on the sync page.
	Name string `yaml:"name"`
	// Role is the membership role given to an address added to the group.
	// "MEMBER" for everyone; the board's own accounts are set up by hand.
	Role string `yaml:"role"`
	// Prune removes addresses that are in the group but not in the registry.
	// The registry is the source of truth, so this defaults to on; turn it off
	// while migrating away from the Apps Script and stray addresses are only
	// reported instead.
	Prune *bool `yaml:"prune"`
	// Keep are addresses the registry never removes, however much it prunes.
	// A group usually holds a few things that are not members — the board's
	// own mailbox, a shared calendar, an archive address — and losing one of
	// those to an over-eager sync would be a genuinely bad afternoon. The
	// three sign-in accounts are protected without being listed, and so is
	// anybody Google says is an owner or a manager rather than a member.
	Keep []string `yaml:"keep"`
}

// Kept reports whether an address is on this group's protected list.
func (g Group) Kept(email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	for _, k := range g.Keep {
		if strings.ToLower(strings.TrimSpace(k)) == email {
			return true
		}
	}
	return false
}

// Pruning reports whether strays should be removed rather than only reported.
func (g Group) Pruning() bool { return g.Prune == nil || *g.Prune }

// MemberRole is the Directory API role for a newly added address.
func (g Group) MemberRole() string {
	if r := strings.ToUpper(strings.TrimSpace(g.Role)); r != "" {
		return r
	}
	return "MEMBER"
}

// GroupFor returns the group that holds a kind of membership.
func (c *Config) GroupFor(k Kind) (Group, bool) {
	for _, g := range c.Groups {
		if g.Kind == k {
			return g, true
		}
	}
	return Group{}, false
}

// Contacts describes the Google Contacts mirror. Every account listed here
// gets the registry copied into its own address book, under a label per kind
// of membership, which is how the board reaches members from their phones.
type Contacts struct {
	// Accounts are the mailboxes whose contacts are kept in step.
	Accounts []string `yaml:"accounts"`
	// Labels names the contact group per kind of membership.
	Labels map[Kind]string `yaml:"labels"`
	// Prune takes the registry's label off cards that carry it but are no
	// longer in the register.
	//
	// It never deletes a card, whatever it is set to. A contact carries a
	// name and a telephone number that may exist nowhere else, and these
	// mailboxes are used for a great deal more than the register — so the
	// most the registry does is take back its own label. Only cards inside
	// that label are looked at at all: somebody's dentist is none of the
	// registry's business.
	Prune *bool `yaml:"prune"`
}

// Pruning reports whether the registry's label should be taken off a stray.
func (c Contacts) Pruning() bool { return c.Prune == nil || *c.Prune }

// LabelFor is the contact-group name for a kind of membership.
func (c Contacts) LabelFor(k Kind) string {
	if name := strings.TrimSpace(c.Labels[k]); name != "" {
		return name
	}
	if k == KindBo {
		return "Bomedlemmar"
	}
	return "Vänmedlemmar"
}

// Enabled reports whether any account asked for a contacts mirror.
func (c Contacts) Enabled() bool { return len(c.Accounts) > 0 }

// Pipeline is how somebody travels from "I saw the house and wondered" to a
// member, and who is looking after them on the way.
//
// It replaces a Focalboard board that lived in Mattermost until the free
// version dropped the feature. The stages are configuration rather than code
// because they are the interview team's own working practice, and a team that
// wants a stage between two others should not need a release.
type Pipeline struct {
	Stages []Stage `yaml:"stages"`
}

// Stage is one column of the board.
type Stage struct {
	ID     string `yaml:"id"`
	Name   string `yaml:"name"`
	NameEN string `yaml:"name_en"`
	// Closed marks a stage where somebody has stopped moving — welcomed, or
	// turned down. The board folds these away by default; they are history
	// rather than work.
	Closed bool `yaml:"closed"`
	// Entry is the stage a form from the public lands in. Exactly one stage
	// has it.
	Entry bool `yaml:"entry"`
}

// NameFor gives the stage's name in one language.
func (s Stage) NameFor(lang string) string { return pick(lang, s.Name, s.NameEN) }

// Stage finds one by id.
func (p Pipeline) Stage(id string) (Stage, bool) {
	for _, s := range p.Stages {
		if s.ID == id {
			return s, true
		}
	}
	return Stage{}, false
}

// EntryStage is where a form from the public arrives.
func (p Pipeline) EntryStage() string {
	for _, s := range p.Stages {
		if s.Entry {
			return s.ID
		}
	}
	if len(p.Stages) > 0 {
		return p.Stages[0].ID
	}
	return "new"
}

// Open are the stages where somebody is still on their way.
func (p Pipeline) Open() []Stage {
	var out []Stage
	for _, s := range p.Stages {
		if !s.Closed {
			out = append(out, s)
		}
	}
	return out
}

// keepFromEnv reads the addresses that must survive a prune but have no
// business in a configuration file.
//
// A keep list is a mixture of two different things. Some entries are the
// association's own mailboxes — admin@, kontakt@, valberedningen@ — which are
// published on the website anyway and belong in the file where anybody can see
// why they are protected. The rest are private addresses of real people who
// happen to be in a group without being in the register, and this repository
// is public. Those go in GROUP_KEEP_BO or GROUP_KEEP_VAN, alongside the other
// deployment-specific settings, and the file keeps only the part that explains
// itself.
//
// It is read here rather than through a call from main, so that no entry point
// can be written that forgets it. Getting this wrong does not fail loudly: it
// quietly shortens the list of people a sync must not touch.
func keepFromEnv(k Kind) []string {
	raw := os.Getenv("GROUP_KEEP_" + strings.ToUpper(string(k)))
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t' || r == '\n'
	})
}

// cleanAddresses lowercases, trims and de-duplicates a list of addresses,
// dropping the empties, so that a keep list assembled from two sources reads
// as one.
func cleanAddresses(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.ToLower(strings.TrimSpace(a))
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// Chat is where the registry announces what happened.
//
// Two channels rather than one, because the two audiences are different. The
// interview team wants to know the moment somebody registers an interest, and
// nothing else; the board wants to see the register change and does not need
// a message every time a stranger fills in the public form.
//
// Channels are given by id rather than by name. A name can be renamed by
// anybody in the channel, and an announcement that quietly stops arriving
// because a channel was renamed is worse than one that fails loudly.
type Chat struct {
	// Candidates is the channel the interview team reads: förmedling.
	Candidates string `yaml:"candidates_channel"`
	// Registry is the channel the board reads: registry-changes.
	Registry string `yaml:"registry_channel"`
	// Mute lists audit actions that are recorded but not announced. It exists
	// because the register's own idea of "a change" is broader than what a
	// channel wants to hear: a cashier ticking off a hundred fees in February
	// is a hundred entries in the trail and would be a hundred messages.
	// Empty means announce everything the trail records.
	Mute []string `yaml:"mute"`
}

// Announces reports whether an audit action should reach the chat.
func (c Chat) Announces(action string) bool {
	for _, m := range c.Mute {
		if strings.EqualFold(strings.TrimSpace(m), action) {
			return false
		}
	}
	return true
}

// Sheet is the spreadsheet the cashiers calculate in. The registry owns the
// tab named here and overwrites it on every run, so nobody should type into
// it — put formulas on a second tab that reads from this one.
type Sheet struct {
	// ID is the spreadsheet id out of its URL.
	ID string `yaml:"id"`
	// Tab is the sheet within it that the registry writes.
	Tab string `yaml:"tab"`
	// Years is how many past years of payments get a column of their own.
	Years int `yaml:"years"`
}

// Enabled reports whether a spreadsheet was configured.
func (s Sheet) Enabled() bool { return strings.TrimSpace(s.ID) != "" }

// TabName is the sheet to write, defaulting to something recognisable.
func (s Sheet) TabName() string {
	if t := strings.TrimSpace(s.Tab); t != "" {
		return t
	}
	return "Medlemmar"
}

// YearColumns is how many years of payment history the sheet carries.
func (s Sheet) YearColumns() int {
	if s.Years > 0 {
		return s.Years
	}
	return 5
}

// Sync is how often the registry reconciles itself with Google, and how
// patient it is when Google says no.
type Sync struct {
	// IntervalMinutes is how often a full reconciliation runs.
	IntervalMinutes int `yaml:"interval_minutes"`
	// SettleSeconds is how long a change waits for its neighbours before
	// triggering a run of its own, so adding five members in a row is one
	// sync rather than five.
	SettleSeconds int `yaml:"settle_seconds"`
	// AlertAfterMinutes is how long an address may fail to synchronise before
	// the board is told about it in as many words. Something that fixes itself
	// on the next run was never worth a red banner.
	AlertAfterMinutes int `yaml:"alert_after_minutes"`
	// MaxRemovalsPerRun is a circuit breaker on the Google groups. A run that
	// wants to take more than this many addresses out of one group does
	// nothing and reports instead.
	//
	// Groups only. The address books have nothing to brake: the registry
	// never deletes a card there, it only takes its own label off, and that
	// is reversible on the next run.
	//
	// The board adds and loses a few members a year. A pass that wants to
	// remove a dozen is not a busy week, it is a mistake — an empty register,
	// a half-finished import, everybody marked as having left — and the
	// difference between reporting that and carrying it out is a quiet
	// evening versus rebuilding a group by hand. Zero turns the brake off.
	MaxRemovalsPerRun int `yaml:"max_removals_per_run"`
	// DotFoldDomains are the mail domains where a dot in the local part means
	// nothing at all, so that anna.andersson@ and annaandersson@ are one
	// mailbox rather than two.
	//
	// This matters more than it sounds. Google folds dots on its own domains,
	// so a group can hold an address in one spelling while the register holds
	// the other. Without folding, every pass would try to add an address
	// Google already has, fail or duplicate, and report a member as broken
	// who is perfectly fine — the exact false alarm that makes a board stop
	// reading the alarms.
	//
	// It is a list rather than a blanket rule because folding where it is not
	// true is the worse mistake: at hotmail.com a.b@ and ab@ really are two
	// different people, and quietly merging them would lose one of them. The
	// Workspace's own domain is added automatically.
	DotFoldDomains []string `yaml:"dot_fold_domains"`
}

// MatchKey is the form an address is compared in — never the form it is
// stored or sent in. Comparison folds case always, and dots in the local part
// on the domains that ignore them.
//
// Everything written to the database or handed to a Google API uses the
// address exactly as the member gave it. Folding is for deciding whether two
// spellings are the same mailbox, and for nothing else.
func (s Sync) MatchKey(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	at := strings.LastIndexByte(email, '@')
	if at <= 0 {
		return email
	}
	local, domain := email[:at], email[at+1:]
	if !s.foldsDots(domain) {
		return email
	}
	// A Gmail address also ignores anything after a plus.
	if plus := strings.IndexByte(local, '+'); plus > 0 {
		local = local[:plus]
	}
	return strings.ReplaceAll(local, ".", "") + "@" + domain
}

func (s Sync) foldsDots(domain string) bool {
	for _, d := range s.DotFoldDomains {
		if strings.EqualFold(strings.TrimSpace(d), domain) {
			return true
		}
	}
	return false
}

// Interval is how often to reconcile, never faster than once a minute.
func (s Sync) Interval() time.Duration {
	m := s.IntervalMinutes
	if m <= 0 {
		m = 10
	}
	if m < 1 {
		m = 1
	}
	return time.Duration(m) * time.Minute
}

// Settle is the quiet period after a change before it is pushed.
func (s Sync) Settle() time.Duration {
	if s.SettleSeconds <= 0 {
		return 5 * time.Second
	}
	return time.Duration(s.SettleSeconds) * time.Second
}

// AlertAfter is how long a failure is tolerated before it is shouted about.
func (s Sync) AlertAfter() time.Duration {
	if s.AlertAfterMinutes <= 0 {
		return 30 * time.Minute
	}
	return time.Duration(s.AlertAfterMinutes) * time.Minute
}

// Location is the timezone every date in the registry is read in.
func (c *Config) Location() *time.Location {
	if c.location != nil {
		return c.location
	}
	return time.UTC
}

// Load reads and validates the YAML configuration at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse reads a configuration from YAML and fills in the defaults.
//
// Unknown fields are refused rather than ignored. A misspelled key in
// config.yaml would otherwise be silently dropped, and the association would
// find out months later that the fee it thought it had set was never read.
func Parse(raw []byte) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	if err := cfg.normalise(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// normalise fills in defaults and refuses anything that cannot work.
func (c *Config) normalise() error {
	if strings.TrimSpace(c.Site.Title) == "" {
		c.Site.Title = "Medlemsregister"
	}
	if c.Site.Language != "sv" && c.Site.Language != "en" {
		c.Site.Language = "sv"
	}
	if strings.TrimSpace(c.Site.Timezone) == "" {
		c.Site.Timezone = "Europe/Stockholm"
	}
	loc, err := time.LoadLocation(c.Site.Timezone)
	if err != nil {
		return fmt.Errorf("unknown timezone %q: %w", c.Site.Timezone, err)
	}
	c.location = loc

	if c.Membership.FeeKr < 0 || c.Membership.FeeKrVan < 0 {
		return fmt.Errorf("the fee cannot be negative")
	}
	if c.Membership.GraceDays < 0 || c.Membership.NewMemberDays < 0 {
		return fmt.Errorf("grace_days and new_member_days cannot be negative")
	}
	if c.Membership.DueOn == "" {
		c.Membership.DueOn = "03-31"
	}
	if c.Membership.NewMemberDays == 0 {
		c.Membership.NewMemberDays = 45
	}

	seen := map[Kind]bool{}
	for i := range c.Groups {
		g := &c.Groups[i]
		g.Email = strings.ToLower(strings.TrimSpace(g.Email))
		if !g.Kind.Valid() {
			return fmt.Errorf("group %q: kind must be %q or %q", g.Email, KindBo, KindVan)
		}
		if g.Email == "" {
			return fmt.Errorf("group for %q has no email", g.Kind)
		}
		if seen[g.Kind] {
			return fmt.Errorf("two groups claim the %q membership; a member belongs in exactly one", g.Kind)
		}
		seen[g.Kind] = true
		if g.Name == "" {
			g.Name = g.Email
		}
		g.Keep = cleanAddresses(append(g.Keep, keepFromEnv(g.Kind)...))
	}

	for i, a := range c.Contacts.Accounts {
		c.Contacts.Accounts[i] = strings.ToLower(strings.TrimSpace(a))
		if c.Contacts.Accounts[i] == "" {
			return fmt.Errorf("contacts.accounts has an empty entry")
		}
	}
	c.Sheet.ID = strings.TrimSpace(c.Sheet.ID)
	c.Chat.Candidates = strings.TrimSpace(c.Chat.Candidates)
	c.Chat.Registry = strings.TrimSpace(c.Chat.Registry)

	if len(c.Pipeline.Stages) == 0 {
		// The stages the interview team already worked in, taken from the
		// board they lost.
		c.Pipeline.Stages = []Stage{
			{ID: "new", Name: "Nya", NameEN: "New", Entry: true},
			{ID: "interview", Name: "Bokad intervju", NameEN: "Interview booked"},
			{ID: "limbo", Name: "Limbo", NameEN: "Limbo"},
			{ID: "welcomed", Name: "Välkomnade", NameEN: "Welcomed", Closed: true},
			{ID: "rejected", Name: "Tackat nej", NameEN: "Declined", Closed: true},
		}
	}
	seenStage := map[string]bool{}
	entries := 0
	for i := range c.Pipeline.Stages {
		st := &c.Pipeline.Stages[i]
		st.ID = strings.ToLower(strings.TrimSpace(st.ID))
		if st.ID == "" {
			return fmt.Errorf("a pipeline stage has no id")
		}
		if seenStage[st.ID] {
			return fmt.Errorf("two pipeline stages share the id %q", st.ID)
		}
		seenStage[st.ID] = true
		if st.Name == "" {
			st.Name = st.ID
		}
		if st.Entry {
			entries++
		}
	}
	if entries > 1 {
		return fmt.Errorf("more than one pipeline stage is marked as the entry")
	}

	if c.Sync.MaxRemovalsPerRun == 0 {
		c.Sync.MaxRemovalsPerRun = 5
	}
	if c.Sync.MaxRemovalsPerRun < 0 {
		c.Sync.MaxRemovalsPerRun = 0 // explicitly off
	}
	if len(c.Sync.DotFoldDomains) == 0 {
		// Google ignores dots on every domain it hosts, which is where most
		// of the association's members have their mail.
		c.Sync.DotFoldDomains = []string{"gmail.com", "googlemail.com"}
	}
	for i, d := range c.Sync.DotFoldDomains {
		c.Sync.DotFoldDomains[i] = strings.ToLower(strings.TrimSpace(d))
	}
	return nil
}

// WithHostedDomain notes the Workspace's own domain as one that folds dots.
// It is Google-hosted, so it does; and it is set from the environment rather
// than from config.yaml, which is why it is added here rather than parsed.
func (c *Config) WithHostedDomain(domain string) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" || c.Sync.foldsDots(domain) {
		return
	}
	c.Sync.DotFoldDomains = append(c.Sync.DotFoldDomains, domain)
}

// Targets lists everything the registry keeps in step, for the sync page and
// for the startup log. It is derived rather than configured, so the page can
// never disagree with what the reconciler actually does.
func (c *Config) Targets() []string {
	var out []string
	for _, g := range c.Groups {
		out = append(out, "group:"+g.Email)
	}
	for _, a := range c.Contacts.Accounts {
		out = append(out, "contacts:"+a)
	}
	if c.Sheet.Enabled() {
		out = append(out, "sheet")
	}
	return out
}
