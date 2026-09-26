package memory

import (
	"fmt"
	"path/filepath"
)

// MinHistorySamples is how many outcomes an agent needs in a directory before
// its track record is trusted for routing.
const MinHistorySamples = 3

// AgentScore is one agent's track record in a directory, used to route tasks.
//
// Successes = accepted runs + benchmark wins.
// Samples   = all runs + benchmark wins + validation-failed benchmarks.
// A benchmark that validated but lost, or skipped validation, is neutral.
type AgentScore struct {
	Successes  int
	Samples    int
	Benchmarks int // how many of Samples came from benchmarks
}

// Score is the Laplace-smoothed acceptance rate (successes+1)/(samples+2).
func (a AgentScore) Score() float64 {
	return float64(a.Successes+1) / float64(a.Samples+2)
}

// better reports whether a scores strictly higher than b, compared exactly as
// fractions so ties are never broken by floating-point noise.
func (a AgentScore) better(b AgentScore) bool {
	return (a.Successes+1)*(b.Samples+2) > (b.Successes+1)*(a.Samples+2)
}

// AgentScores returns every agent's track record for dir (exact match).
func (s *Store) AgentScores(dir string) (map[string]AgentScore, error) {
	out := map[string]AgentScore{}
	rows, err := s.db.Query(`SELECT agent, COUNT(*), SUM(outcome = 'accepted') FROM runs WHERE dir = ? GROUP BY agent`, dir)
	if err != nil {
		return nil, fmt.Errorf("agent scores: %w", err)
	}
	for rows.Next() {
		var name string
		var n, acc int
		if err := rows.Scan(&name, &n, &acc); err != nil {
			rows.Close()
			return nil, err
		}
		sc := out[name]
		sc.Successes += acc
		sc.Samples += n
		out[name] = sc
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = s.db.Query(
		`SELECT agent, SUM(won = 1), SUM(won = 0 AND valid = 0 AND skipped = 0) FROM benchmarks WHERE dir = ? GROUP BY agent`, dir)
	if err != nil {
		return nil, fmt.Errorf("agent scores: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var wins, fails int
		if err := rows.Scan(&name, &wins, &fails); err != nil {
			return nil, err
		}
		sc := out[name]
		sc.Successes += wins
		sc.Samples += wins + fails
		sc.Benchmarks += wins + fails
		out[name] = sc
	}
	return out, rows.Err()
}

// BestAgent picks the candidate with the best track record in dir. It only
// picks when an agent has at least MinHistorySamples outcomes, more successes
// than failures, and a strictly higher score than every other qualifying
// candidate; a tie or any error yields ok=false. The reason explains the pick,
// e.g. "claude: 8/10 accepted in this dir". Safe on a nil Store.
func (s *Store) BestAgent(dir string, candidates []string) (name, reason string, ok bool) {
	if s == nil || len(candidates) == 0 {
		return "", "", false
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs // runs are recorded under the absolute directory
	}
	scores, err := s.AgentScores(dir)
	if err != nil {
		return "", "", false
	}
	var best AgentScore
	tied := false
	for _, c := range candidates {
		sc, found := scores[c]
		if !found || sc.Samples < MinHistorySamples || 2*sc.Successes <= sc.Samples {
			continue
		}
		switch {
		case name == "" || sc.better(best):
			name, best, tied = c, sc, false
		case !best.better(sc):
			tied = true
		}
	}
	if name == "" || tied {
		return "", "", false
	}
	reason = fmt.Sprintf("%s: %d/%d accepted in this dir", name, best.Successes, best.Samples)
	if best.Benchmarks > 0 {
		reason += " (runs + benchmarks)"
	}
	return name, reason, true
}
