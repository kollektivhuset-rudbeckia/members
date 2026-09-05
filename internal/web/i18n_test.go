package web

import (
	"io/fs"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/i18n"
	"github.com/kollektivhuset-rudbeckia/members/internal/membership"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
	"github.com/kollektivhuset-rudbeckia/members/internal/sync"
)

// A phrase the templates ask for but the catalogue does not have renders as
// ⟦key⟧ on the page. That is loud, but it should never get as far as a
// person: these tests read the templates and the code and fail the build.

var (
	// {{t "some.key"}} and {{t "some.key" arg}}, including inside an argument
	// list such as (t "x") or dict "Label" (t "y").
	tCall = regexp.MustCompile(`\bt\s+"([a-z0-9._]+)"`)
	// count "member" 3 / plural "year" 1 — the unit names catalogue rows too.
	unitCall = regexp.MustCompile(`\b(?:count|plural)\s+"([a-z0-9._]+)"`)
)

func templateSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := fs.WalkDir(templateFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".html") {
			return err
		}
		b, err := fs.ReadFile(templateFS, path)
		if err != nil {
			return err
		}
		out[path] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no templates found")
	}
	return out
}

func TestEveryPhraseTheTemplatesAskForExists(t *testing.T) {
	var missing []string
	for path, src := range templateSources(t) {
		for _, m := range tCall.FindAllStringSubmatch(src, -1) {
			if !i18n.Has(m[1]) {
				missing = append(missing, path+": "+m[1])
			}
		}
		for _, m := range unitCall.FindAllStringSubmatch(src, -1) {
			for _, suffix := range []string{".one", ".many"} {
				if key := "unit." + m[1] + suffix; !i18n.Has(key) {
					missing = append(missing, path+": "+key)
				}
			}
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("phrase is missing from the catalogue — %s", m)
	}
}

// The same for the phrases the Go code asks for. An error message only shows
// up when something has gone wrong, which is the worst possible moment to
// discover that its key was mistyped.
func TestEveryPhraseTheCodeAsksForExists(t *testing.T) {
	call := regexp.MustCompile(`i18n\.T\(\s*[a-zA-Z0-9_.]+,\s*"([a-z0-9._]+)"`)
	unit := regexp.MustCompile(`i18n\.(?:Count|Plural)\(\s*[a-zA-Z0-9_.]+,\s*"([a-z0-9._]+)"`)
	// The tests run in the package directory, so ".." is internal/: every
	// package that says anything to a person is under it.
	sources := os.DirFS("..")
	var missing []string
	err := fs.WalkDir(sources, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		b, err := fs.ReadFile(sources, path)
		if err != nil {
			return err
		}
		for _, m := range call.FindAllStringSubmatch(string(b), -1) {
			if !i18n.Has(m[1]) {
				missing = append(missing, path+": "+m[1])
			}
		}
		for _, m := range unit.FindAllStringSubmatch(string(b), -1) {
			for _, suffix := range []string{".one", ".many"} {
				if key := "unit." + m[1] + suffix; !i18n.Has(key) {
					missing = append(missing, path+": "+key)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("phrase is missing from the catalogue — %s", m)
	}
}

// The keys the templates build by hand — {{t (printf "kind.%s" .Kind)}} — are
// invisible to the regular expression above, because the key does not exist
// until the page renders. Every set of values that goes into one is
// enumerated here instead, which is also the list to extend when a new kind
// of membership, role or sync trigger arrives.
func TestEveryComputedPhraseExists(t *testing.T) {
	var want []string

	for _, k := range config.Kinds {
		want = append(want, "kind."+string(k), "kind."+string(k)+".plural")
	}
	for _, r := range config.Roles {
		want = append(want, "role."+string(r), "role."+string(r)+".can")
	}
	for _, f := range []membership.FeeState{membership.Paid, membership.Due,
		membership.Overdue, membership.Ended} {
		want = append(want, "fee."+string(f))
	}
	for _, s := range []store.ProposalStatus{store.Pending, store.Approved,
		store.Rejected, store.Withdrawn, store.Stale} {
		want = append(want, "proposal."+string(s))
	}
	for _, k := range []store.ProposalKind{store.ProposeUpdate, store.ProposeDelete} {
		want = append(want, "proposal.kind."+string(k))
	}
	for _, tr := range sync.Triggers {
		want = append(want, "sync.trigger."+string(tr))
	}
	want = append(want, "target.group", "target.contacts", "target.sheet")
	// The audit trail's actions, which the log page renders by name.
	for _, a := range []string{
		"member.added", "member.changed", "member.removed", "member.imported",
		"payment.recorded", "payment.withdrawn",
		"proposal.update", "proposal.delete", "proposal.approved", "proposal.rejected",
	} {
		want = append(want, "audit."+a)
	}

	for _, key := range want {
		if !i18n.Has(key) {
			t.Errorf("computed phrase is missing from the catalogue — %s", key)
		}
	}
}

// Both columns of the catalogue have to be filled in. A blank translation
// renders as nothing at all, which is worse than the wrong language.
func TestEveryPhraseHasBothLanguages(t *testing.T) {
	for _, key := range i18n.Keys() {
		sv, en, _ := i18n.Entry(key)
		if strings.TrimSpace(sv) == "" {
			t.Errorf("%s has no Swedish", key)
		}
		if strings.TrimSpace(en) == "" {
			t.Errorf("%s has no English", key)
		}
	}
}

// A phrase with a %s in one language and none in the other would render the
// argument in Swedish and swallow it in English — or, worse, print
// "%!s(MISSING)" at somebody.
func TestBothLanguagesTakeTheSameArguments(t *testing.T) {
	verb := regexp.MustCompile(`%[a-zA-Z]`)
	for _, key := range i18n.Keys() {
		sv, en, _ := i18n.Entry(key)
		gotSV, gotEN := verb.FindAllString(sv, -1), verb.FindAllString(en, -1)
		if strings.Join(gotSV, "") != strings.Join(gotEN, "") {
			t.Errorf("%s takes %v in Swedish but %v in English", key, gotSV, gotEN)
		}
	}
}

// A stray Swedish sentence in a template is invisible until somebody reads
// the English site and finds half of it in Swedish. Text outside a {{...}}
// action is the giveaway.
func TestTemplatesHoldNoLooseText(t *testing.T) {
	// Everything between actions, minus the punctuation and markup that
	// legitimately sits there.
	action := regexp.MustCompile(`\{\{[^}]*\}\}`)
	tag := regexp.MustCompile(`<[^>]*>`)
	// A <code> block holds a command or an address, not prose. Translating
	// "members -import" would be a bug, not a courtesy.
	code := regexp.MustCompile(`(?s)<code>.*?</code>`)
	swedish := regexp.MustCompile(`[A-Za-zÅÄÖåäö]{4,}`)

	// Words that are the same in both languages, or are not words at all.
	fine := map[string]bool{
		"doctype": true, "html": true, "utf": true, "true": true, "false": true,
		"Rudbeckia": true, "Google": true, "medlemsregister": true,
	}

	for path, src := range templateSources(t) {
		stripped := tag.ReplaceAllString(
			code.ReplaceAllString(action.ReplaceAllString(src, ""), ""), "")
		for _, word := range swedish.FindAllString(stripped, -1) {
			if fine[word] {
				continue
			}
			t.Errorf("%s has loose text outside the catalogue: %q", path, word)
		}
	}
}
