package importer

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kollektivhuset-rudbeckia/members/internal/auth"
	"github.com/kollektivhuset-rudbeckia/members/internal/config"
	"github.com/kollektivhuset-rudbeckia/members/internal/store"
)

// The interview team kept their candidates on a Focalboard board inside
// Mattermost until the free version dropped the feature. What survives is a
// plain-text dump of it, and this reads that dump.
//
// It is a one-off by design. The format is whatever the export happened to
// produce — headings in capitals for the stages, indented "Label: value"
// lines for the fields — and there will never be a second one. What matters
// is that it is careful about what it does not understand: a line it cannot
// place is reported rather than dropped, because the whole point is that the
// board is not lost.

// Candidate is one row read out of the export.
type CandidateRow struct {
	Name        string
	Email       string
	Phone       string
	Apartment   string
	Interview   string
	Responsible string
	Stage       string
	// Note is carried onto the candidate, for anything that did not fit a
	// field of its own.
	Note string
	// Problem says why this one cannot be imported at all, which is rare.
	Problem string
	// Warning is imported but worth a look.
	Warning string
	// Settled marks somebody the old board had already finished with —
	// welcomed, or turned down. They are not brought across: the pipeline is
	// for people on their way, and starting it with three times as many
	// finished cards as live ones would bury the work.
	Settled bool
	// Existing is set when somebody with that address is already on the board.
	Existing bool
}

// Importable reports whether this row would be written.
func (c CandidateRow) Importable() bool { return c.Problem == "" && !c.Existing && !c.Settled }

// blank reports whether nothing at all was recorded.
func (c CandidateRow) blank() bool {
	return c.Name == "" && c.Email == "" && c.Phone == "" &&
		c.Apartment == "" && c.Interview == "" && c.Responsible == ""
}

// BoardResult is what a board import found and did.
type BoardResult struct {
	Rows     []CandidateRow
	Imported int
	DryRun   bool
	// Stages maps the headings in the file to the stages in config.yaml, so a
	// heading nobody expected is visible rather than silently becoming "new".
	Stages map[string]string
}

// Ready, Problems and Skipped split the rows for the report.
func (r BoardResult) Ready() []CandidateRow { return filterRows(r.Rows, CandidateRow.Importable) }
func (r BoardResult) Problems() []CandidateRow {
	return filterRows(r.Rows, func(c CandidateRow) bool { return c.Problem != "" })
}

// Settled are the ones the old board had finished with.
func (r BoardResult) Settled() []CandidateRow {
	return filterRows(r.Rows, func(c CandidateRow) bool { return c.Settled })
}

// Warnings are the rows that go in but want a second look.
func (r BoardResult) Warnings() []CandidateRow {
	return filterRows(r.Rows, func(c CandidateRow) bool { return c.Warning != "" && c.Problem == "" })
}
func (r BoardResult) Skipped() []CandidateRow {
	return filterRows(r.Rows, func(c CandidateRow) bool { return c.Existing && c.Problem == "" })
}

func filterRows(list []CandidateRow, keep func(CandidateRow) bool) []CandidateRow {
	var out []CandidateRow
	for _, c := range list {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// stageFor maps a heading from the export onto a stage in the configuration.
//
// The headings are the ones the team typed into Focalboard, so they are
// matched loosely and by hand. Anything unrecognised lands in the entry stage
// with a note saying where it came from, which is better than inventing a
// stage nobody configured or dropping the person.
func stageFor(heading string, p config.Pipeline) (string, bool) {
	h := strings.ToLower(strings.TrimSpace(heading))
	switch {
	case strings.Contains(h, "booked") || strings.Contains(h, "intervju"):
		return "interview", true
	case strings.Contains(h, "new"):
		return "new", true
	case strings.Contains(h, "limbo"):
		return "limbo", true
	case strings.Contains(h, "welcom"):
		return "welcomed", true
	case strings.Contains(h, "reject"):
		return "rejected", true
	}
	return p.EntryStage(), false
}

// ReadBoard parses the export.
//
// The whole file is read first so that a line can look at the one after it.
// That matters: a heading is only a heading because the next line is a row of
// dashes, and without the lookahead the heading itself gets read as a person —
// which is how "WELCOMED" briefly became a candidate.
func ReadBoard(r io.Reader, p config.Pipeline) ([]CandidateRow, map[string]string, error) {
	var lines []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}

	var (
		rows      []CandidateRow
		current   *CandidateRow
		stage     = p.EntryStage()
		mapping   = map[string]string{}
		inBody    bool
		seenStage bool
	)
	// Anything at all was recorded about them: the board is full of cards
	// with a flat and an interview date and no name, and those are people the
	// team knows perfectly well.
	flush := func() {
		if current != nil && !current.blank() {
			rows = append(rows, *current)
		}
		current = nil
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// A heading is a line with a rule under it.
		if isRule(next(lines, i)) && trimmed != "" {
			flush()
			// The prose at the top — "What this is", "Summary" — is titled in
			// sentence case; the stages are in capitals. Once the first stage
			// has gone by, every later heading is a stage whatever its case:
			// the export has one called FILED UNDER "Nina Ekstrand",
			// created by mistake, and the two people under it are still
			// people.
			if isStageHeading(trimmed) || seenStage {
				seenStage = true
				mapped, known := stageFor(trimmed, p)
				stage = mapped
				if known {
					mapping[trimmed] = stage
				} else {
					mapping[trimmed] = stage + " (okänd rubrik)"
				}
				inBody = true
			} else {
				inBody = false
			}
			continue
		}
		// The rule itself.
		if isRule(trimmed) {
			continue
		}
		if !inBody {
			continue
		}
		if trimmed == "" {
			flush()
			continue
		}

		// "    Label: value" is a field of the candidate above it.
		if label, value, ok := field(line); ok && current != nil {
			switch label {
			case "email":
				current.Email = store.Email(value)
			case "phone":
				current.Phone = value
			case "apartment":
				current.Apartment = value
			case "interview booked":
				current.Interview = value
			case "responsible":
				current.Responsible = value
			}
			continue
		}

		// Anything else at candidate indentation starts a new one — but only
		// if fields follow it. A section can open with a sentence explaining
		// itself, and a sentence is not a person.
		if indent(line) <= 6 && startsACard(lines, i) {
			flush()
			name := trimmed
			if strings.EqualFold(name, "(no name entered)") {
				name = ""
			}
			current = &CandidateRow{Name: name, Stage: stage}
		}
	}
	flush()
	return rows, mapping, nil
}

// startsACard reports whether the line at i is a candidate's name, which it
// is when the next non-blank line is one of their fields, indented further.
func startsACard(lines []string, i int) bool {
	here := indent(lines[i])
	for j := i + 1; j < len(lines); j++ {
		if strings.TrimSpace(lines[j]) == "" {
			return false // a blank line ends the card before it began
		}
		if indent(lines[j]) <= here {
			return false
		}
		_, _, ok := field(lines[j])
		return ok
	}
	return false
}

func next(lines []string, i int) string {
	if i+1 < len(lines) {
		return strings.TrimSpace(lines[i+1])
	}
	return ""
}

// isRule reports whether a line is the row of dashes under a heading.
func isRule(s string) bool {
	return len(s) > 10 && strings.Trim(s, "-=") == ""
}

// isStageHeading tells a stage from the prose above it. The stages were typed
// in capitals by whatever wrote the export; the explanatory sections were not.
func isStageHeading(s string) bool {
	hasLetter := false
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			return false
		}
		if r >= 'A' && r <= 'Z' {
			hasLetter = true
		}
	}
	return hasLetter
}

// field reads an indented "Label: value" line.
func field(line string) (label, value string, ok bool) {
	if indent(line) < 6 {
		return "", "", false
	}
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	label = strings.ToLower(strings.TrimSpace(line[:i]))
	value = strings.TrimSpace(line[i+1:])
	if label == "" || strings.ContainsAny(label, "@") {
		return "", "", false
	}
	return label, value, true
}

func indent(line string) int {
	n := 0
	for _, r := range line {
		if r != ' ' && r != '\t' {
			break
		}
		n++
	}
	return n
}

// ImportBoard reads the export and writes the candidates.
func ImportBoard(ctx context.Context, st *store.Store, cfg *config.Config,
	r io.Reader, dryRun bool, actor string) (BoardResult, error) {

	rows, mapping, err := ReadBoard(r, cfg.Pipeline)
	if err != nil {
		return BoardResult{}, fmt.Errorf("read the export: %w", err)
	}
	result := BoardResult{Rows: rows, DryRun: dryRun, Stages: mapping}

	existing := map[string]bool{}
	current, err := st.Candidates(ctx, cfg.Location())
	if err != nil {
		return result, fmt.Errorf("read the board: %w", err)
	}
	for _, c := range current {
		if c.Email != "" {
			existing[cfg.Sync.MatchKey(c.Email)] = true
		}
	}

	// The point of this import is that the board is not lost, so almost
	// nothing blocks a row. A duplicate or an odd address is a note for the
	// team to sort out on a board they are looking at anyway — losing the
	// person instead would be the one unrecoverable outcome.
	closed := map[string]bool{}
	for _, st := range cfg.Pipeline.Stages {
		if st.Closed {
			closed[st.ID] = true
		}
	}

	seen := map[string]bool{}
	for i := range result.Rows {
		row := &result.Rows[i]

		// Welcomed and turned down are both out of the process. Whoever was
		// welcomed is in the member register by now, which is the record that
		// matters; whoever was turned down does not need carrying forward.
		if closed[row.Stage] {
			row.Settled = true
			continue
		}

		// Some cards carry two people and two addresses in one field. Neither
		// is a valid address, so the field is emptied and the text kept where
		// the team will read it.
		if row.Email != "" && !oneAddress(row.Email) {
			row.Note = "e-post i exporten: " + row.Email
			row.Warning = "två adresser i ett fält — dela upp dem"
			row.Email = ""
		}

		switch {
		case row.blank():
			row.Problem = "ingenting alls antecknat"
		case row.Email == "":
			// Kept all the same: the team knows who this is, and a card with
			// a flat and an interview date and no name is most of this board.
		case existing[cfg.Sync.MatchKey(row.Email)]:
			row.Existing = true
		case seen[cfg.Sync.MatchKey(row.Email)]:
			row.Warning = "samma adress finns på fler än ett kort i exporten"
			seen[cfg.Sync.MatchKey(row.Email)] = true
		default:
			seen[cfg.Sync.MatchKey(row.Email)] = true
		}
	}
	if dryRun {
		return result, nil
	}

	now := time.Now()
	for _, row := range result.Rows {
		if !row.Importable() {
			continue
		}
		first, last := splitName(row.Name)
		c := store.Candidate{
			ID: auth.ID(), Token: auth.Token(),
			FirstName: first, LastName: last, Email: row.Email, Phone: row.Phone,
			Apartment: row.Apartment, Kind: config.KindVan, Stage: row.Stage,
			Responsible: row.Responsible, Note: row.Note, Source: store.SourceImport,
			CreatedAt: now, MovedAt: now, MovedBy: actor,
		}
		if row.Interview != "" {
			if t, err := store.ParseDay(row.Interview, cfg.Location()); err == nil {
				c.InterviewOn.Time, c.InterviewOn.Valid = t, true
			}
		}
		if err := st.CreateCandidate(ctx, c); err != nil {
			return result, fmt.Errorf("write %s: %w", row.Name, err)
		}
		result.Imported++
	}
	return result, nil
}

// oneAddress reports whether a field holds a single address rather than two
// people's, which some cards on the old board did.
func oneAddress(s string) bool {
	s = strings.TrimSpace(s)
	return !strings.ContainsAny(s, ", ;") && strings.Count(s, "@") == 1
}

// splitName divides a name the way candidateOf does, on the last space.
func splitName(name string) (first, last string) {
	name = strings.TrimSpace(name)
	if i := strings.LastIndexByte(name, ' '); i > 0 {
		return name[:i], name[i+1:]
	}
	return name, ""
}
