package importer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

func pipeline(t *testing.T) config.Pipeline {
	t.Helper()
	cfg, err := config.Parse([]byte(`
site: {title: Test, language: sv, timezone: Europe/Stockholm}
groups:
  - {kind: bo, email: bo@example.test}
`))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Pipeline
}

const export = `CANDIDATE LIST - Rudbeckia
========================================================================

What this is
------------------------------------------------------------------------
This is the full contents of the "Candidates" board.


BOOKED INTERVJU
------------------------------------------------------------------------

  Ove Bergkvist
      Apartment:         1401
      Email:             ove.bergkvist@example.test
      Interview booked:  2025-09-30

  Siv Hallgren
      Apartment:         1401
      Interview booked:  2023-11-28
      Responsible:       Karin Dahl


WELCOMED
------------------------------------------------------------------------

  Mira Lindqvist
      Apartment:         1401
      Email:             mira.lindqvist@example.test
      Interview booked:  2023-11-28
      Responsible:       Tati


REJECTED
------------------------------------------------------------------------

  (no name entered)
      Apartment:         1504
      Phone:             0700000000
      Email:             okand@example.test
`

func TestReadingTheExportedBoard(t *testing.T) {
	rows, mapping, err := ReadBoard(strings.NewReader(export), pipeline(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("got %d candidates, want 4: %+v", len(rows), rows)
	}

	// The prose at the top must not become a candidate.
	for _, r := range rows {
		if strings.HasPrefix(r.Name, "This is the full") {
			t.Error("the explanation at the top was read as a person")
		}
	}

	acki := rows[0]
	if acki.Name != "Ove Bergkvist" {
		t.Errorf("name: got %q", acki.Name)
	}
	if acki.Email != "ove.bergkvist@example.test" {
		t.Errorf("email: got %q", acki.Email)
	}
	if acki.Apartment != "1401" {
		t.Errorf("apartment: got %q", acki.Apartment)
	}
	if acki.Interview != "2025-09-30" {
		t.Errorf("interview: got %q", acki.Interview)
	}
	if acki.Stage != "interview" {
		t.Errorf("stage: got %q, want interview", acki.Stage)
	}

	// A candidate with no address at all is still on the board, and the
	// heading still places them.
	if rows[1].Email != "" || rows[1].Responsible != "Karin Dahl" {
		t.Errorf("the second candidate: %+v", rows[1])
	}
	if rows[2].Stage != "welcomed" {
		t.Errorf("Emma's stage: got %q", rows[2].Stage)
	}
	// "(no name entered)" is not a name.
	if rows[3].Name != "" {
		t.Errorf("the unnamed candidate got the placeholder as a name: %q", rows[3].Name)
	}
	if rows[3].Phone != "0700000000" {
		t.Errorf("phone: got %q", rows[3].Phone)
	}

	for heading, stage := range map[string]string{
		"BOOKED INTERVJU": "interview", "WELCOMED": "welcomed", "REJECTED": "rejected",
	} {
		if mapping[heading] != stage {
			t.Errorf("%q mapped to %q, want %q", heading, mapping[heading], stage)
		}
	}
}

// A heading nobody expected must be visible rather than silently becoming the
// entry stage — the export has one that was created by mistake.
func TestAnUnknownHeadingIsReported(t *testing.T) {
	const odd = export + `

FILED UNDER "Nina Ekstrand"
------------------------------------------------------------------------

  Someone Else
      Email:             someone@example.test
`
	rows, mapping, err := ReadBoard(strings.NewReader(odd), pipeline(t))
	if err != nil {
		t.Fatal(err)
	}
	last := rows[len(rows)-1]
	if last.Name != "Someone Else" {
		t.Fatalf("the odd heading lost its candidate: %+v", rows)
	}
	if last.Stage != "new" {
		t.Errorf("stage: got %q, want the entry stage", last.Stage)
	}
	var noted string
	for heading, stage := range mapping {
		if strings.Contains(heading, "Nina") {
			noted = stage
		}
	}
	if !strings.Contains(noted, "okänd") {
		t.Errorf("the unexpected heading was not flagged: %q", noted)
	}
}

// The real thing, if it is to hand. It is the only copy of the board there is,
// so a parser that quietly loses a third of it would be worse than useless.
func TestTheRealExportIfItIsHere(t *testing.T) {
	raw, err := os.ReadFile(os.Getenv("HOME") + "/rudbeckia-candidates.txt")
	if err != nil {
		t.Skip("the export is not on this machine")
	}
	rows, mapping, err := ReadBoard(strings.NewReader(string(raw)), pipeline(t))
	if err != nil {
		t.Fatal(err)
	}
	// The file's own summary says 41.
	if len(rows) != 41 {
		t.Errorf("got %d candidates, and the export says there are 41", len(rows))
	}
	withEmail, withStage := 0, map[string]int{}
	for _, r := range rows {
		if r.Email != "" {
			withEmail++
		}
		withStage[r.Stage]++
	}
	t.Logf("read %d candidates, %d with an address, by stage %v", len(rows), withEmail, withStage)
	t.Logf("headings mapped: %v", mapping)
	if withEmail < 25 {
		t.Errorf("only %d have an address; the export has 32 Email lines", withEmail)
	}
}

// Welcomed and turned down are out of the process. Bringing them across would
// start the board with three times as many finished cards as live ones.
func TestTheSettledOnesAreNotBroughtAcross(t *testing.T) {
	cfg, err := config.Parse([]byte(`
site: {title: Test, language: sv, timezone: Europe/Stockholm}
groups:
  - {kind: bo, email: bo@example.test}
`))
	if err != nil {
		t.Fatal(err)
	}
	st := openStore(t)

	result, err := ImportBoard(context.Background(), st, cfg, strings.NewReader(export), true, "ny@example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range result.Ready() {
		if row.Stage == "welcomed" || row.Stage == "rejected" {
			t.Errorf("%s is in a closed stage and would still be imported", row.Name)
		}
	}
	if len(result.Settled()) != 2 {
		t.Errorf("got %d settled rows, want 2 (Emma welcomed, the unnamed one rejected)", len(result.Settled()))
	}
	// And the live ones still come.
	if len(result.Ready()) != 2 {
		t.Errorf("got %d ready, want the 2 with interviews booked", len(result.Ready()))
	}
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "board.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
