package memory

import (
	"database/sql"
	"math"
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

func TestUsageRoundTripAndAgentTotals(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now()
	for _, r := range []Run{
		{Dir: "/p", Agent: "api", Prompt: "one", Outcome: "accepted", Attempts: 1, TokensIn: 12_000, TokensOut: 800, CostUSD: 0.05},
		{Dir: "/p", Agent: "api", Prompt: "two", Outcome: "rejected", Attempts: 2, TokensIn: 3_000, TokensOut: 200, CostUSD: 0.01},
		{Dir: "/p", Agent: "cli", Prompt: "three", Outcome: "accepted", Attempts: 1},
	} {
		if err := s.Record(r, now); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := s.Recent("/p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if r := runs[2]; r.TokensIn != 12_000 || r.TokensOut != 800 || r.CostUSD != 0.05 {
		t.Fatalf("run usage not round-tripped: %+v", r)
	}
	if r := runs[0]; r.TokensIn != 0 || r.CostUSD != 0 {
		t.Fatalf("cli run should have no usage: %+v", r)
	}

	stats, err := s.StatsByAgent("/p")
	if err != nil {
		t.Fatal(err)
	}
	if st := stats["api"]; st.TokensIn != 15_000 || st.TokensOut != 1_000 || math.Abs(st.CostUSD-0.06) > 1e-9 {
		t.Fatalf("api totals: %+v", st)
	}
	if st := stats["cli"]; st.TokensIn != 0 || st.CostUSD != 0 {
		t.Fatalf("cli totals: %+v", st)
	}

	if err := s.RecordBenchmark(BenchRun{Dir: "/p", Task: "t", Agent: "api", Valid: true, Won: true,
		TokensIn: 4_000, TokensOut: 300, CostUSD: 0.02}, now); err != nil {
		t.Fatal(err)
	}
	benches, err := s.RecentBenchmarks("/p", 5)
	if err != nil || len(benches) != 1 {
		t.Fatalf("benches=%+v err=%v", benches, err)
	}
	if b := benches[0]; b.TokensIn != 4_000 || b.TokensOut != 300 || b.CostUSD != 0.02 {
		t.Fatalf("bench usage not round-tripped: %+v", b)
	}
}

// A database from before usage tracking (runs without usage columns, and a
// benchmarks table without them) upgrades in place, idempotently, and old rows
// read back as zero usage.
func TestMigrateAddsUsageColumnsToOldSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE runs (
    id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, dir TEXT NOT NULL, agent TEXT NOT NULL,
    prompt TEXT NOT NULL, outcome TEXT NOT NULL, attempts INTEGER NOT NULL, passed INTEGER NOT NULL,
    diff TEXT NOT NULL DEFAULT '');
CREATE TABLE benchmarks (
    id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, dir TEXT NOT NULL, task TEXT NOT NULL,
    agent TEXT NOT NULL, valid INTEGER NOT NULL, skipped INTEGER NOT NULL, changed INTEGER NOT NULL,
    duration_ms INTEGER NOT NULL, retries INTEGER NOT NULL, files INTEGER NOT NULL, added INTEGER NOT NULL,
    removed INTEGER NOT NULL, exit INTEGER NOT NULL, won INTEGER NOT NULL);
INSERT INTO runs (ts, dir, agent, prompt, outcome, attempts, passed) VALUES ('2026-01-01T00:00:00Z', '/p', 'a', 'old', 'accepted', 1, 1);
INSERT INTO benchmarks (ts, dir, task, agent, valid, skipped, changed, duration_ms, retries, files, added, removed, exit, won)
    VALUES ('2026-01-01T00:00:00Z', '/p', 'old task', 'a', 1, 0, 1, 1500, 0, 1, 2, 1, 0, 1);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open #%d: %v", i+1, err)
		}
		runs, rerr := s.Recent("/p", 10)
		benches, berr := s.RecentBenchmarks("/p", 10)
		stats, serr := s.StatsByAgent("/p")
		if rerr != nil || berr != nil || serr != nil {
			t.Fatalf("open #%d: %v %v %v", i+1, rerr, berr, serr)
		}
		if len(runs) != 1 || runs[0].TokensIn != 0 || runs[0].CostUSD != 0 {
			t.Fatalf("open #%d: runs=%+v", i+1, runs)
		}
		if len(benches) != 1 || benches[0].Task != "old task" || benches[0].TokensOut != 0 {
			t.Fatalf("open #%d: benches=%+v", i+1, benches)
		}
		if stats["a"].Runs != 1 || stats["a"].TokensIn != 0 {
			t.Fatalf("open #%d: stats=%+v", i+1, stats)
		}
		// New rows with usage can be written into the upgraded tables.
		if err := s.Record(Run{Dir: "/q", Agent: "b", Prompt: "new", Outcome: "accepted", Attempts: 1, TokensIn: 10, TokensOut: 5, CostUSD: 0.001}, time.Now()); err != nil {
			t.Fatalf("open #%d: record: %v", i+1, err)
		}
		if err := s.RecordBenchmark(BenchRun{Dir: "/q", Task: "new", Agent: "b", TokensIn: 10, TokensOut: 5, CostUSD: 0.001}, time.Now()); err != nil {
			t.Fatalf("open #%d: record benchmark: %v", i+1, err)
		}
		s.Close()
	}
}
