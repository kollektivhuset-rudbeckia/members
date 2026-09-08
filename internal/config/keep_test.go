package config

import (
	"strings"
	"testing"
)

const keepYAML = `
site: {title: Test, language: sv, timezone: Europe/Stockholm}
groups:
  - kind: bo
    email: bomedlemmar@example.test
    prune: true
    keep:
      - admin@example.test
  - kind: van
    email: friends@example.test
    prune: true
`

// A keep list is assembled from two places: the association's own mailboxes,
// which explain themselves and belong in the tracked file, and private
// addresses of people who are in a group without being in the register, which
// do not belong in a public repository. Both have to end up in the same list,
// because a sync that sees only half of it prunes real people.
func TestTheKeepListIsAssembledFromFileAndEnvironment(t *testing.T) {
	t.Setenv("GROUP_KEEP_BO", "Someone@Example.COM, other@example.com")
	t.Setenv("GROUP_KEEP_VAN", "third@example.com")

	cfg, err := Parse([]byte(keepYAML))
	if err != nil {
		t.Fatal(err)
	}
	bo, ok := cfg.GroupFor(KindBo)
	if !ok {
		t.Fatal("no bo group")
	}
	for _, want := range []string{"admin@example.test", "someone@example.com", "other@example.com"} {
		if !bo.Kept(want) {
			t.Errorf("the bo keep list does not protect %q: %v", want, bo.Keep)
		}
	}
	// The environment must not leak across groups.
	if bo.Kept("third@example.com") {
		t.Error("a van address ended up protecting a bo address")
	}
	van, _ := cfg.GroupFor(KindVan)
	if !van.Kept("third@example.com") {
		t.Errorf("the van keep list does not protect its own address: %v", van.Keep)
	}
	if van.Kept("someone@example.com") {
		t.Error("a bo address ended up in the van keep list")
	}
}

// Case and stray whitespace are how a hand-edited .env actually looks.
func TestTheEnvironmentKeepListIsForgiving(t *testing.T) {
	t.Setenv("GROUP_KEEP_BO", "  A@Example.com ,, B@example.com ; c@example.com\n")
	cfg, err := Parse([]byte(keepYAML))
	if err != nil {
		t.Fatal(err)
	}
	bo, _ := cfg.GroupFor(KindBo)
	for _, want := range []string{"a@example.com", "b@example.com", "c@example.com"} {
		if !bo.Kept(want) {
			t.Errorf("does not protect %q: %v", want, bo.Keep)
		}
	}
	for _, got := range bo.Keep {
		if strings.TrimSpace(got) != got || got == "" {
			t.Errorf("a keep entry was not cleaned: %q", got)
		}
	}
}

// An unset variable must leave the file's own list exactly as it was, and in
// particular must not add an empty entry — an empty string in a keep list
// would match nothing, but it is the kind of thing that hides a bug.
func TestNoEnvironmentMeansTheFileAlone(t *testing.T) {
	t.Setenv("GROUP_KEEP_BO", "")
	cfg, err := Parse([]byte(keepYAML))
	if err != nil {
		t.Fatal(err)
	}
	bo, _ := cfg.GroupFor(KindBo)
	if len(bo.Keep) != 1 || bo.Keep[0] != "admin@example.test" {
		t.Errorf("keep list: got %v, want just the file's entry", bo.Keep)
	}
}

// The same address in both places is one entry, not two.
func TestAnAddressInBothPlacesIsNotDuplicated(t *testing.T) {
	t.Setenv("GROUP_KEEP_BO", "admin@example.test")
	cfg, err := Parse([]byte(keepYAML))
	if err != nil {
		t.Fatal(err)
	}
	bo, _ := cfg.GroupFor(KindBo)
	if len(bo.Keep) != 1 {
		t.Errorf("keep list: got %v, want one entry", bo.Keep)
	}
}
