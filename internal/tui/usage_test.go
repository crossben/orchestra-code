package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/memory"
)

// usageModel is a model with history, benchmarks and agent stats where the
// api agent reported usage and the cli agent did not.
func usageModel(w, h int) Model {
	m := testModelIn(".", nil, nil, w, h)
	m.runs = []memory.Run{
		{Agent: "alpha", Prompt: "api run", Outcome: "accepted", Attempts: 2, TokensIn: 12_345, TokensOut: 1_100, CostUSD: 0.042},
		{Agent: "beta", Prompt: "cli run", Outcome: "rejected", Attempts: 1},
	}
	m.benches = []memory.BenchRow{
		{Agent: "alpha", Task: "race", Valid: true, Won: true, Duration: time.Second, TokensIn: 4_000, TokensOut: 300, CostUSD: 0.02},
		{Agent: "beta", Task: "race", Duration: 2 * time.Second},
	}
	m.stats = map[string]memory.AgentStats{
		"alpha": {Runs: 2, Accepted: 1, LastUsed: time.Now(), TokensIn: 15_000, TokensOut: 1_000, CostUSD: 0.06},
		"beta":  {Runs: 1, LastUsed: time.Now()},
	}
	m.reload()
	return m
}

func mustShow(t *testing.T, view string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(view, w) {
			t.Fatalf("view missing %q\n---\n%s", w, view)
		}
	}
}

func TestUsageColumnsRender(t *testing.T) {
	m := usageModel(160, 30)
	mustShow(t, m.switchTab(tabHistory).View(), "TOKENS", "COST", "12.3k/1.1k", "$0.04", "—")
	mustShow(t, m.switchTab(tabBench).View(), "TOKENS", "COST", "4.0k/300", "$0.02")
	mustShow(t, m.switchTab(tabAgents).View(), "TOKENS", "COST", "15.0k/1.0k", "$0.06")
}

// With only CLI agents (no usage anywhere) the columns stay out of the way.
func TestUsageColumnsHiddenWithoutUsage(t *testing.T) {
	m := testModelIn(".", nil, nil, 160, 30)
	m.runs = []memory.Run{{Agent: "beta", Prompt: "cli run", Outcome: "accepted", Attempts: 1}}
	m.benches = []memory.BenchRow{{Agent: "beta", Task: "race"}}
	m.reload()
	for _, tb := range []tab{tabHistory, tabBench, tabAgents} {
		if v := m.switchTab(tb).View(); strings.Contains(v, "TOKENS") {
			t.Fatalf("tab %s should hide usage columns without usage:\n%s", tabNames[tb], v)
		}
	}
}

// On narrow terminals low-priority columns give way; tokens stay visible in
// History and Benchmarks and nothing overflows.
func TestUsageColumnsFitNarrowWindows(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {100, 30}, {140, 40}} {
		m := usageModel(size[0], size[1])
		for _, tb := range []tab{tabHistory, tabBench, tabAgents} {
			v := m.switchTab(tb).View()
			for i, l := range strings.Split(v, "\n") {
				if w := lipgloss.Width(l); w > size[0] {
					t.Fatalf("%dx%d tab %s line %d is %d wide:\n%s", size[0], size[1], tabNames[tb], i, w, l)
				}
			}
			if tb != tabAgents {
				mustShow(t, v, "TOKENS")
			}
		}
	}
}

func TestLiveRunPanelShowsUsage(t *testing.T) {
	m := testModelIn(".", nil, nil, 140, 40)
	m.cstate = chatRunning
	m.run = &liveRun{start: time.Now(), agent: "alpha"}
	u := agent.Usage{InputTokens: 1000, OutputTokens: 100, CostUSD: 0.0045, Known: true, Priced: true}
	for _, e := range []engine.Event{
		{Kind: engine.EventAttempt, Attempt: 1, Max: 2},
		{Kind: engine.EventAgentDone, Attempt: 1, Max: 2, Usage: u},
		{Kind: engine.EventAttempt, Attempt: 2, Max: 2},
		{Kind: engine.EventAgentDone, Attempt: 2, Max: 2, Usage: u},
	} {
		m.applyEvent(e)
	}
	mustShow(t, m.View(), "tokens 2.0k in / 200 out · $0.0090")
}

// A reviewed turn's summed usage lands in History and the agent's totals.
func TestFinishReviewRecordsUsage(t *testing.T) {
	m := testModel()
	m.pending = engine.Turn{Attempts: 2, Usage: agent.Usage{InputTokens: 2000, OutputTokens: 200, CostUSD: 0.009, Known: true, Priced: true}}
	m.pendAg, m.pendTask = "alpha", "task"
	m = m.finishReview("accepted")
	if len(m.runs) != 1 || m.runs[0].TokensIn != 2000 || m.runs[0].TokensOut != 200 || m.runs[0].CostUSD != 0.009 {
		t.Fatalf("run not recorded with usage: %+v", m.runs)
	}
	if st := m.stats["alpha"]; st.TokensIn != 2000 || st.CostUSD != 0.009 {
		t.Fatalf("agent totals not updated: %+v", st)
	}
	mustShow(t, m.switchTab(tabHistory).View(), "2.0k/200", "$0.0090")
}

func TestLiveRunPanelHidesUnknownUsage(t *testing.T) {
	m := testModelIn(".", nil, nil, 140, 40)
	m.cstate = chatRunning
	m.run = &liveRun{start: time.Now(), agent: "alpha"}
	m.applyEvent(engine.Event{Kind: engine.EventAttempt, Attempt: 1, Max: 1})
	m.applyEvent(engine.Event{Kind: engine.EventAgentDone, Attempt: 1, Max: 1})
	if strings.Contains(m.View(), "tokens") {
		t.Fatalf("unknown usage must not render:\n%s", m.View())
	}
}
