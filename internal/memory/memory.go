// Package memory persists Orchestra's execution history in SQLite: what was run,
// which agent, and how it turned out. It powers `orchestra history` and a
// preferred-agent hint (the agent whose changes you accept most often for a
// project).
//
// The database lives OUTSIDE the working tree (~/.orchestra/orchestra.db by
// default), keyed by absolute project directory — writing it into the repo would
// dirty the tree and trip the supervised clean-tree guard.
package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a handle to the memory database.
type Store struct {
	db *sql.DB
}

// Run is one recorded execution.
type Run struct {
	ID       int64
	Time     time.Time
	Dir      string
	Agent    string
	Prompt   string
	Outcome  string // "accepted" | "rejected" | "no-change" | "failed"
	Attempts int
	Passed   bool // validation passed
}

// DefaultPath returns ~/.orchestra/orchestra.db.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".orchestra", "orchestra.db"), nil
}

// Open opens (creating if needed) the store at path and ensures the schema.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS runs (
    id       INTEGER PRIMARY KEY AUTOINCREMENT,
    ts       TEXT    NOT NULL,
    dir      TEXT    NOT NULL,
    agent    TEXT    NOT NULL,
    prompt   TEXT    NOT NULL,
    outcome  TEXT    NOT NULL,
    attempts INTEGER NOT NULL,
    passed   INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_runs_dir ON runs(dir);`)
	return err
}

// Record stores a run. now is passed in (the workflow layer stamps time).
func (s *Store) Record(r Run, now time.Time) error {
	passed := 0
	if r.Passed {
		passed = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO runs (ts, dir, agent, prompt, outcome, attempts, passed) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		now.UTC().Format(time.RFC3339), r.Dir, r.Agent, r.Prompt, r.Outcome, r.Attempts, passed,
	)
	return err
}

// Recent returns the most recent runs for dir (all dirs if dir == "").
func (s *Store) Recent(dir string, limit int) ([]Run, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if dir == "" {
		rows, err = s.db.Query(`SELECT id, ts, dir, agent, prompt, outcome, attempts, passed FROM runs ORDER BY id DESC LIMIT ?`, limit)
	} else {
		rows, err = s.db.Query(`SELECT id, ts, dir, agent, prompt, outcome, attempts, passed FROM runs WHERE dir = ? ORDER BY id DESC LIMIT ?`, dir, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		var r Run
		var ts string
		var passed int
		if err := rows.Scan(&r.ID, &ts, &r.Dir, &r.Agent, &r.Prompt, &r.Outcome, &r.Attempts, &passed); err != nil {
			return nil, err
		}
		r.Time, _ = time.Parse(time.RFC3339, ts)
		r.Passed = passed == 1
		out = append(out, r)
	}
	return out, rows.Err()
}

// PreferredAgent returns the agent with the most accepted runs for dir, and how
// many. Empty name means no history yet.
func (s *Store) PreferredAgent(dir string) (string, int, error) {
	row := s.db.QueryRow(
		`SELECT agent, COUNT(*) c FROM runs WHERE dir = ? AND outcome = 'accepted' GROUP BY agent ORDER BY c DESC LIMIT 1`,
		dir,
	)
	var name string
	var count int
	switch err := row.Scan(&name, &count); err {
	case nil:
		return name, count, nil
	case sql.ErrNoRows:
		return "", 0, nil
	default:
		return "", 0, fmt.Errorf("preferred agent: %w", err)
	}
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }
