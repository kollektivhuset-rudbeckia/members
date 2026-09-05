// Package store persists the member register in SQLite.
//
// Dates that describe a day in the association's life — when somebody joined,
// when a fee landed — are stored as plain "2006-01-02" strings, because that
// is what they are: a day, not an instant. Timestamps that record when the
// software did something are stored as UTC RFC3339. Both stay readable in the
// sqlite3 CLI, which is the only tool anybody will have to hand at the point
// they need to look.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/kollektivhuset-rudbeckia/members/internal/config"
)

// Store is the persistence handle.
type Store struct {
	db *sql.DB
}

// ErrNotFound is returned when an id does not exist.
var ErrNotFound = errors.New("no such record")

// ErrDuplicateEmail is returned when an address is already in the register.
// It is the one clash worth its own error: the address is the key everything
// syncs on, so two members cannot share one.
var ErrDuplicateEmail = errors.New("that e-mail address is already in the register")

const schema = `
CREATE TABLE IF NOT EXISTS members (
	id           TEXT PRIMARY KEY,
	first_name   TEXT NOT NULL DEFAULT '',
	last_name    TEXT NOT NULL DEFAULT '',
	email        TEXT NOT NULL,
	phone        TEXT NOT NULL DEFAULT '',
	kind         TEXT NOT NULL,
	apartment    TEXT NOT NULL DEFAULT '',
	joined_on    TEXT NOT NULL,
	left_on      TEXT,
	also_in      TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT '',
	created_at   TEXT NOT NULL,
	created_by   TEXT NOT NULL DEFAULT '',
	updated_at   TEXT NOT NULL,
	updated_by   TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_members_email ON members (email);
CREATE INDEX IF NOT EXISTS idx_members_kind ON members (kind, left_on);

CREATE TABLE IF NOT EXISTS payments (
	member_id     TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
	year          INTEGER NOT NULL,
	amount_kr     INTEGER NOT NULL DEFAULT 0,
	paid_on       TEXT NOT NULL,
	method        TEXT NOT NULL DEFAULT '',
	note          TEXT NOT NULL DEFAULT '',
	registered_by TEXT NOT NULL DEFAULT '',
	registered_at TEXT NOT NULL,
	PRIMARY KEY (member_id, year)
);
CREATE INDEX IF NOT EXISTS idx_payments_year ON payments (year);

CREATE TABLE IF NOT EXISTS proposals (
	id            TEXT PRIMARY KEY,
	member_id     TEXT NOT NULL,
	kind          TEXT NOT NULL,
	before_json   TEXT NOT NULL DEFAULT '',
	after_json    TEXT NOT NULL DEFAULT '',
	reason        TEXT NOT NULL DEFAULT '',
	status        TEXT NOT NULL,
	proposed_by   TEXT NOT NULL,
	proposed_at   TEXT NOT NULL,
	decided_by    TEXT NOT NULL DEFAULT '',
	decided_at    TEXT,
	decision_note TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_proposals_status ON proposals (status, proposed_at);
CREATE INDEX IF NOT EXISTS idx_proposals_member ON proposals (member_id, status);

CREATE TABLE IF NOT EXISTS audit (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	at        TEXT NOT NULL,
	actor     TEXT NOT NULL DEFAULT '',
	role      TEXT NOT NULL DEFAULT '',
	action    TEXT NOT NULL,
	member_id TEXT NOT NULL DEFAULT '',
	subject   TEXT NOT NULL DEFAULT '',
	detail    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_audit_at ON audit (at DESC);
CREATE INDEX IF NOT EXISTS idx_audit_member ON audit (member_id, at DESC);

CREATE TABLE IF NOT EXISTS sync_state (
	target        TEXT NOT NULL,
	address       TEXT NOT NULL,
	member_id     TEXT NOT NULL DEFAULT '',
	intent        TEXT NOT NULL DEFAULT '',
	ok            INTEGER NOT NULL DEFAULT 1,
	message       TEXT NOT NULL DEFAULT '',
	failing_since TEXT,
	last_ok       TEXT,
	last_try      TEXT NOT NULL,
	PRIMARY KEY (target, address)
);
CREATE INDEX IF NOT EXISTS idx_sync_state_ok ON sync_state (ok, target);

CREATE TABLE IF NOT EXISTS sync_runs (
	id          INTEGER PRIMARY KEY AUTOINCREMENT,
	started_at  TEXT NOT NULL,
	finished_at TEXT NOT NULL,
	trigger     TEXT NOT NULL DEFAULT '',
	target      TEXT NOT NULL,
	ok          INTEGER NOT NULL DEFAULT 1,
	added       INTEGER NOT NULL DEFAULT 0,
	removed     INTEGER NOT NULL DEFAULT 0,
	updated     INTEGER NOT NULL DEFAULT 0,
	failed      INTEGER NOT NULL DEFAULT 0,
	message     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sync_runs_started ON sync_runs (started_at DESC);
`

// Open opens (and if needed creates) the database at path.
func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	// _txlock=immediate makes every transaction take the write lock up front.
	// Without it two writers can both start as readers and then collide when
	// they try to upgrade, which SQLite reports as SQLITE_BUSY_SNAPSHOT and
	// busy_timeout cannot retry away.
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite handles one writer at a time; a small pool avoids lock churn.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if _, err := db.Exec(schema); err != nil {
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	if err := migrate(db); err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrate adds columns that arrived after the first release. A register that
// has been running since before a column existed keeps its members and its
// history; the column is added empty, which is the right default for every
// one of them so far.
func migrate(db *sql.DB) error {
	have, err := columns(db, "members")
	if err != nil {
		return err
	}
	// also_in: the groups a member belongs to beyond their own kind. A
	// bomedlem who runs a matlag needs to reach the vänmedlemmar, and before
	// this column that was done by hand in a keep-list that nobody could see.
	if !have["also_in"] {
		if _, err := db.Exec(`ALTER TABLE members ADD COLUMN also_in TEXT NOT NULL DEFAULT ''`); err != nil {
			return fmt.Errorf("add column also_in: %w", err)
		}
	}
	return nil
}

func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read the columns of %s: %w", table, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// Email normalises an address into the form the register keys on: trimmed and
// lowercased. Google is case-insensitive about the local part in practice and
// the register has to agree with it, or the same person is added twice.
func Email(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func utc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// day formats a date as the plain day it is.
func day(t time.Time) string { return t.Format("2006-01-02") }

// ParseDay reads a stored "2006-01-02" in the association's own timezone, so
// that "joined on the 3rd" means midnight in Uppsala rather than in London.
func ParseDay(s string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
}

// isUnique reports whether an error is SQLite complaining about the unique
// index on the address. The driver gives no typed error for it, so the text
// is all there is to go on — and both spellings appear in the wild.
func isUnique(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") || strings.Contains(msg, "constraint failed: members.email")
}

// mustKind narrows a stored value to a membership the code knows, so a
// hand-edited database cannot make a template render an empty column.
func mustKind(s string) config.Kind {
	if k, ok := config.ParseKind(s); ok {
		return k
	}
	return config.KindVan
}
