package config

import (
	"strings"
	"testing"
	"time"
)

func parse(t *testing.T, yaml string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cfg
}

const minimal = `
site: {title: Test, language: sv, timezone: Europe/Stockholm}
membership: {fee_kr: 200}
groups:
  - {kind: bo, email: Bomedlemmar@Rudbeckia.NU}
  - {kind: van, email: friends@rudbeckia.nu}
`

func TestParseNormalisesAndFillsInDefaults(t *testing.T) {
	cfg := parse(t, minimal)

	if got := cfg.Groups[0].Email; got != "bomedlemmar@rudbeckia.nu" {
		t.Errorf("the group address was not lowercased: %q", got)
	}
	if !cfg.Groups[0].Pruning() {
		t.Error("a group should prune by default; the register is the source of truth")
	}
	if got := cfg.Membership.DueOn; got != "03-31" {
		t.Errorf("due date default: got %q, want 03-31", got)
	}
	if got := cfg.Membership.NewMemberDays; got != 45 {
		t.Errorf("new member allowance default: got %d, want 45", got)
	}
	if got := cfg.Sync.Interval(); got != 10*time.Minute {
		t.Errorf("sync interval default: got %s, want 10m", got)
	}
	// The labels have to match what the association already uses in Google
	// Contacts, or the register makes a second set beside the real one.
	if got := cfg.Contacts.LabelFor(KindBo); got != "Bomedlemmar" {
		t.Errorf("bo label default: got %q, want Bomedlemmar", got)
	}
	if got := cfg.Contacts.LabelFor(KindVan); got != "Vänmedlemmar" {
		t.Errorf("van label default: got %q, want Vänmedlemmar", got)
	}
}

// A misspelled key would otherwise be dropped in silence, and the association
// would find out months later that the fee it set was never read.
func TestParseRefusesAnUnknownKey(t *testing.T) {
	_, err := Parse([]byte(minimal + "\nmembership: {fee_kronor: 200}\n"))
	if err == nil {
		t.Fatal("a misspelled key was accepted")
	}
}

func TestParseRefusesTwoGroupsForOneMembership(t *testing.T) {
	_, err := Parse([]byte(`
site: {title: Test, language: sv, timezone: Europe/Stockholm}
groups:
  - {kind: bo, email: one@example.test}
  - {kind: bo, email: two@example.test}
`))
	if err == nil {
		t.Fatal("two groups claimed the same membership and were accepted")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Errorf("the error should say why: %v", err)
	}
}

// Gmail ignores dots and everything after a plus. Getting this wrong makes
// the register try to add an address Google already has, over and over,
// reporting a perfectly fine member as broken every ten minutes.
func TestMatchKeyFoldsDotsOnlyWhereGoogleDoes(t *testing.T) {
	cfg := parse(t, minimal)
	cfg.WithHostedDomain("rudbeckia.nu")

	tests := []struct{ in, want string }{
		{"Anna.Andersson@Gmail.com", "annaandersson@gmail.com"},
		{"annaandersson@gmail.com", "annaandersson@gmail.com"},
		{"anna.andersson+forening@gmail.com", "annaandersson@gmail.com"},
		{"a.b@googlemail.com", "ab@googlemail.com"},
		{"styrelsen@rudbeckia.nu", "styrelsen@rudbeckia.nu"},
		{"sty.relsen@rudbeckia.nu", "styrelsen@rudbeckia.nu"},
		// Not a Google domain: a.b@ and ab@ really are two different people
		// at hotmail, and merging them would lose one of them.
		{"Anna.Andersson@hotmail.com", "anna.andersson@hotmail.com"},
		{"  Anna@Example.TEST  ", "anna@example.test"},
		{"nonsense", "nonsense"},
		{"", ""},
	}
	for _, tc := range tests {
		if got := cfg.Sync.MatchKey(tc.in); got != tc.want {
			t.Errorf("MatchKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMatchKeyIsSymmetric(t *testing.T) {
	cfg := parse(t, minimal)
	a := cfg.Sync.MatchKey("Anna.Andersson@gmail.com")
	b := cfg.Sync.MatchKey("annaandersson@GMAIL.com")
	if a != b {
		t.Errorf("two spellings of one mailbox gave %q and %q", a, b)
	}
}

// Losing the board's own mailbox to an over-eager prune would be a genuinely
// bad afternoon.
func TestKeptProtectsAnAddressFromPruning(t *testing.T) {
	cfg := parse(t, `
site: {title: Test, language: sv, timezone: Europe/Stockholm}
groups:
  - kind: bo
    email: bomedlemmar@rudbeckia.nu
    keep: [Admin@Rudbeckia.nu, kontakt@rudbeckia.nu]
`)
	g := cfg.Groups[0]
	for _, address := range []string{"admin@rudbeckia.nu", "ADMIN@rudbeckia.nu", " kontakt@rudbeckia.nu "} {
		if !g.Kept(address) {
			t.Errorf("%q should be protected", address)
		}
	}
	if g.Kept("someone@example.test") {
		t.Error("an ordinary address should not be protected")
	}
}

func TestDueDateReadsTheConfiguredDay(t *testing.T) {
	cfg := parse(t, minimal)
	cfg.Membership.DueOn = "05-15"
	got := cfg.Membership.DueDate(2026, cfg.Location())
	want := time.Date(2026, time.May, 15, 0, 0, 0, 0, cfg.Location())
	if !got.Equal(want) {
		t.Errorf("got %s, want %s", got, want)
	}

	// Nonsense falls back rather than panicking or producing month zero.
	cfg.Membership.DueOn = "not-a-date"
	if got := cfg.Membership.DueDate(2026, cfg.Location()); got.Month() != time.March {
		t.Errorf("a broken due_on should fall back to March: got %s", got)
	}
}

func TestFeeForFallsBackToTheSharedFee(t *testing.T) {
	cfg := parse(t, minimal)
	if got := cfg.Membership.FeeFor(KindVan); got != 200 {
		t.Errorf("with no separate friend fee: got %d, want 200", got)
	}
	cfg.Membership.FeeKrVan = 100
	if got := cfg.Membership.FeeFor(KindVan); got != 100 {
		t.Errorf("with a separate friend fee: got %d, want 100", got)
	}
	if got := cfg.Membership.FeeFor(KindBo); got != 200 {
		t.Errorf("resident fee should be unaffected: got %d, want 200", got)
	}
}

func TestTargetsAreDerivedFromTheConfiguration(t *testing.T) {
	cfg := parse(t, minimal+`
contacts:
  accounts: [styrelsen@rudbeckia.nu]
sheet:
  id: abc123
`)
	want := []string{
		"group:bomedlemmar@rudbeckia.nu",
		"group:friends@rudbeckia.nu",
		"contacts:styrelsen@rudbeckia.nu",
		"sheet",
	}
	got := cfg.Targets()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("target %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

// The permission matrix is the association's own division of labour, and it
// is the one thing in the register that must not drift.
func TestAccessMatrix(t *testing.T) {
	access := Access{PaymentRoles: map[Role]bool{RoleCashier: true}}

	cases := []struct {
		role Role
		perm Permission
		want bool
	}{
		// Everybody sees the register and may write down somebody new. That
		// is the point: a member is added the moment they show interest.
		{RoleIntake, PermView, true},
		{RoleCashier, PermView, true},
		{RoleBoard, PermView, true},
		{RoleIntake, PermAdd, true},
		{RoleCashier, PermAdd, true},
		{RoleBoard, PermAdd, true},

		// Only the cashier touches money. They are the one with the bank
		// statement in front of them.
		{RoleIntake, PermPay, false},
		{RoleCashier, PermPay, true},
		{RoleBoard, PermPay, false},

		// Changing somebody already in the register.
		{RoleIntake, PermEdit, false},
		{RoleCashier, PermEdit, true},
		{RoleBoard, PermEdit, true},

		// Removing somebody is the board's alone; everybody else proposes it.
		{RoleIntake, PermDelete, false},
		{RoleCashier, PermDelete, false},
		{RoleBoard, PermDelete, true},

		{RoleIntake, PermPropose, true},
		{RoleCashier, PermPropose, true},
		{RoleBoard, PermPropose, false},

		{RoleIntake, PermApprove, false},
		{RoleCashier, PermApprove, false},
		{RoleBoard, PermApprove, true},

		{RoleIntake, PermSync, false},
		{RoleBoard, PermSync, true},

		// Nobody who has not signed in may do anything at all.
		{RoleNone, PermView, false},
		{RoleNone, PermAdd, false},
	}
	for _, tc := range cases {
		if got := access.May(tc.role, tc.perm); got != tc.want {
			t.Errorf("%s may %s: got %v, want %v", tc.role, tc.perm, got, tc.want)
		}
	}
}

func TestNeedsApprovalIsWhatSomebodyCannotDoOutright(t *testing.T) {
	access := Access{PaymentRoles: map[Role]bool{RoleCashier: true}}

	if !access.NeedsApproval(RoleIntake, PermEdit) {
		t.Error("an edit by intake should become a proposal")
	}
	if !access.NeedsApproval(RoleIntake, PermDelete) {
		t.Error("a removal by intake should become a proposal")
	}
	if !access.NeedsApproval(RoleCashier, PermDelete) {
		t.Error("a removal by the cashier should become a proposal")
	}
	if access.NeedsApproval(RoleCashier, PermEdit) {
		t.Error("the cashier edits outright")
	}
	if access.NeedsApproval(RoleBoard, PermDelete) {
		t.Error("the board never asks anybody")
	}
}

// The house asked for payments to be the cashier's alone, which is the
// default. An association that wants the board to be able to fix a slip says
// so with one environment variable, and this is that switch.
func TestPaymentRolesCanBeWidened(t *testing.T) {
	access := Access{PaymentRoles: map[Role]bool{RoleCashier: true, RoleBoard: true}}
	if !access.May(RoleBoard, PermPay) {
		t.Error("the board should be able to record a payment when configured to")
	}
	if access.May(RoleIntake, PermPay) {
		t.Error("intake still should not")
	}
}
