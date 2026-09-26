package engine

import (
	"bufio"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/validate"
)

// meteredAgent is an in-process fake API-style agent: every attempt reports
// the same token usage; the first attempt writes a failing file, later ones
// fix it (so validation forces exactly one retry).
type meteredAgent struct {
	calls int
	usage agent.Usage
	fail  error // returned (with usage) on every call when set
}

func (m *meteredAgent) Name() string  { return "metered" }
func (m *meteredAgent) Health() error { return nil }
func (m *meteredAgent) Capabilities() []agent.Capability {
	return []agent.Capability{agent.CapImplement}
}
func (m *meteredAgent) Run(_ context.Context, task agent.Task) (agent.Result, error) {
	m.calls++
	if m.fail != nil {
		return agent.Result{Usage: m.usage}, m.fail
	}
	content := "broken\n"
	if m.calls >= 2 {
		content = "fixed\n"
	}
	if err := os.WriteFile(filepath.Join(task.Dir, "out.txt"), []byte(content), 0o644); err != nil {
		return agent.Result{}, err
	}
	return agent.Result{Output: "ok", Usage: m.usage}, nil
}

var attemptUsage = agent.Usage{InputTokens: 1000, OutputTokens: 100, CostUSD: 0.0045, Known: true, Priced: true}

var fixedCheck = []validate.Stage{{Name: "test", Command: "grep -q fixed out.txt"}}

func TestProduceSumsUsageAcrossRetries(t *testing.T) {
	var perAttempt []agent.Usage
	turn := Produce(context.Background(), Options{
		Agent: &meteredAgent{usage: attemptUsage}, Prompt: "fix", Dir: t.TempDir(),
		Stages: fixedCheck, MaxRetries: 2,
		OnEvent: func(e Event) {
			if e.Kind == EventAgentDone {
				perAttempt = append(perAttempt, e.Usage)
			}
		},
	})
	if turn.Err != nil || turn.Attempts != 2 {
		t.Fatalf("turn: attempts=%d err=%v", turn.Attempts, turn.Err)
	}
	u := turn.Usage
	if !u.Known || !u.Priced || u.InputTokens != 2000 || u.OutputTokens != 200 || math.Abs(u.CostUSD-0.009) > 1e-12 {
		t.Fatalf("summed usage = %+v", u)
	}
	if len(perAttempt) != 2 || perAttempt[0] != attemptUsage || perAttempt[1] != attemptUsage {
		t.Fatalf("EventAgentDone should carry each attempt's usage: %+v", perAttempt)
	}
}

// Tokens spent on an attempt whose agent call failed still count.
func TestProduceKeepsUsageOnAgentError(t *testing.T) {
	turn := Produce(context.Background(), Options{
		Agent: &meteredAgent{usage: attemptUsage, fail: errors.New("apply failed")}, Prompt: "x", Dir: t.TempDir(),
	})
	if turn.Err == nil || turn.Usage != attemptUsage {
		t.Fatalf("err=%v usage=%+v", turn.Err, turn.Usage)
	}
}

func TestExecuteSumsUsageAndRecordsIt(t *testing.T) {
	mem, err := memory.Open(filepath.Join(t.TempDir(), "o.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	out, err := Execute(context.Background(), bufio.NewReader(strings.NewReader("y\n")), Options{
		Agent: &meteredAgent{usage: attemptUsage}, Prompt: "fix", Dir: t.TempDir(),
		Stages: fixedCheck, MaxRetries: 2, Memory: mem,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Attempts != 2 || out.Usage.InputTokens != 2000 || out.Usage.OutputTokens != 200 {
		t.Fatalf("outcome usage: attempts=%d %+v", out.Attempts, out.Usage)
	}
	runs, err := mem.Recent("", 5)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if r := runs[0]; r.TokensIn != 2000 || r.TokensOut != 200 || math.Abs(r.CostUSD-0.009) > 1e-12 {
		t.Fatalf("recorded run usage: %+v", r)
	}
}

// CLI-style agents report nothing: usage stays unknown end to end.
func TestProduceUnknownUsageForCLIAgents(t *testing.T) {
	turn := Produce(context.Background(), Options{Agent: noopAgent{}, Prompt: "x", Dir: t.TempDir()})
	if turn.Usage.Known || turn.Usage.String() != "" {
		t.Fatalf("usage should be unknown: %+v", turn.Usage)
	}
}
