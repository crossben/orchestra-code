package memory

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordRoundTripsDiff(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	if err := s.Record(Run{Dir: "/p", Agent: "a", Prompt: "x", Outcome: "accepted", Attempts: 1, Passed: true, Diff: "diff --git a/f b/f\n+hi\n"}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(Run{Dir: "/p", Agent: "b", Prompt: "y", Outcome: "rejected", Attempts: 2}, now); err != nil {
		t.Fatal(err)
	}
	runs, err := s.Recent("/p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[1].Diff != "diff --git a/f b/f\n+hi\n" || runs[0].Diff != "" {
		t.Fatalf("diff not round-tripped: %+v", runs)
	}

	stats, err := s.StatsByAgent("/p")
	if err != nil {
		t.Fatal(err)
	}
	if stats["a"].Runs != 1 || stats["a"].Accepted != 1 || stats["b"].Accepted != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

// A database created before the diff column existed must be upgraded in place,
// and opening it again must not fail (the migration is idempotent).
func TestMigrateAddsDiffToOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, dir TEXT NOT NULL, agent TEXT NOT NULL,
    prompt TEXT NOT NULL, outcome TEXT NOT NULL, attempts INTEGER NOT NULL, passed INTEGER NOT NULL);
INSERT INTO runs (ts, dir, agent, prompt, outcome, attempts, passed) VALUES ('2026-01-01T00:00:00Z', '/p', 'a', 'old', 'accepted', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		runs, err := s.Recent("/p", 10)
		s.Close()
		if err != nil || len(runs) != 1 || runs[0].Prompt != "old" || runs[0].Diff != "" {
			t.Fatalf("open #%d: runs=%+v err=%v", i+1, runs, err)
		}
	}
}
