package memory

import (
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// runs records n runs for agent in dir, the first `accepted` of them accepted.
func runs(t *testing.T, s *Store, dir, agent string, accepted, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		outcome := "rejected"
		if i < accepted {
			outcome = "accepted"
		}
		if err := s.Record(Run{Dir: dir, Agent: agent, Prompt: "p", Outcome: outcome, Attempts: 1}, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
}

func bench(t *testing.T, s *Store, dir, agent string, valid, skipped, won bool) {
	t.Helper()
	if err := s.RecordBenchmark(BenchRun{Dir: dir, Task: "t", Agent: agent, Valid: valid, Skipped: skipped, Won: won}, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAgentScoresCombinesRunsAndBenchmarks(t *testing.T) {
	s := openTemp(t)
	runs(t, s, "/p", "claude", 2, 3)
	bench(t, s, "/p", "claude", true, false, true)   // win → success
	bench(t, s, "/p", "claude", false, false, false) // validation failed → reject
	bench(t, s, "/p", "claude", true, false, false)  // valid but lost → neutral
	bench(t, s, "/p", "claude", false, true, false)  // validation skipped → neutral
	runs(t, s, "/other", "claude", 5, 5)             // different dir: ignored

	got, err := s.AgentScores("/p")
	if err != nil {
		t.Fatal(err)
	}
	c := got["claude"]
	if c.Successes != 3 || c.Samples != 5 {
		t.Fatalf("claude = %+v, want 3/5", c)
	}
	if want := 4.0 / 7.0; c.Score() != want {
		t.Fatalf("score = %v, want %v", c.Score(), want)
	}
}

func TestBestAgent(t *testing.T) {
	all := []string{"claude", "opencode", "mimo"}
	cases := []struct {
		name       string
		setup      func(*Store)
		candidates []string
		want       string
		wantReason string
		wantOK     bool
	}{
		{name: "no history", setup: func(*Store) {}, candidates: all},
		{name: "below min samples", setup: func(s *Store) { runs(t, s, "/p", "claude", 2, 2) }, candidates: all},
		{
			name: "clear winner",
			setup: func(s *Store) {
				runs(t, s, "/p", "claude", 8, 10)
				runs(t, s, "/p", "opencode", 1, 3)
			},
			candidates: all, want: "claude", wantReason: "claude: 8/10 accepted in this dir", wantOK: true,
		},
		{
			name: "only candidates considered",
			setup: func(s *Store) {
				runs(t, s, "/p", "claude", 8, 10)
				runs(t, s, "/p", "opencode", 2, 3)
			},
			candidates: []string{"opencode", "mimo"}, want: "opencode", wantReason: "opencode: 2/3 accepted in this dir", wantOK: true,
		},
		{
			name: "tie means no pick",
			setup: func(s *Store) {
				runs(t, s, "/p", "claude", 3, 3)
				runs(t, s, "/p", "opencode", 3, 3)
			},
			candidates: all,
		},
		{
			name:       "poor record is never pushed",
			setup:      func(s *Store) { runs(t, s, "/p", "claude", 1, 4) },
			candidates: all,
		},
		{
			name: "benchmark wins count",
			setup: func(s *Store) {
				runs(t, s, "/p", "mimo", 1, 1)
				bench(t, s, "/p", "mimo", true, false, true)
				bench(t, s, "/p", "mimo", true, false, true)
			},
			candidates: all, want: "mimo", wantReason: "mimo: 3/3 accepted in this dir (runs + benchmarks)", wantOK: true,
		},
		{
			name:       "other dir ignored",
			setup:      func(s *Store) { runs(t, s, "/elsewhere", "claude", 5, 5) },
			candidates: all,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTemp(t)
			tc.setup(s)
			name, reason, ok := s.BestAgent("/p", tc.candidates)
			if ok != tc.wantOK || name != tc.want || reason != tc.wantReason {
				t.Fatalf("BestAgent = (%q, %q, %v), want (%q, %q, %v)", name, reason, ok, tc.want, tc.wantReason, tc.wantOK)
			}
		})
	}
}

func TestBestAgentNilStore(t *testing.T) {
	var s *Store
	if _, _, ok := s.BestAgent("/p", []string{"claude"}); ok {
		t.Fatal("nil store must not pick")
	}
}
