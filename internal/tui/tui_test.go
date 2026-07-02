package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/config"
)

func testModel() Model {
	reg := agent.NewRegistry()
	reg.Add(agent.New("alpha", "true", nil, "", []agent.Capability{agent.CapImplement}))
	reg.Add(agent.New("beta", "true", nil, "", []agent.Capability{agent.CapReview}))
	cfg := &config.Config{DefaultAgent: "alpha"}
	return New(cfg, reg, nil, ".") // nil memory: history/benchmarks empty
}

func key(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestViewRendersAgentsTab(t *testing.T) {
	m := testModel()
	view := m.View()
	for _, want := range []string{"ORCHESTRA", "Agents", "History", "Benchmarks", "AGENT", "alpha", "beta", "quit"} {
		if !strings.Contains(view, want) {
			t.Fatalf("agents view missing %q\n---\n%s", want, view)
		}
	}
}

func TestTabSwitching(t *testing.T) {
	m := testModel()
	// switch to History (key "2")
	nm, _ := m.Update(key("2"))
	m = nm.(Model)
	if m.active != tabHistory {
		t.Fatalf("expected History tab, got %d", m.active)
	}
	if !strings.Contains(m.View(), "no run history") {
		t.Fatalf("history view should show empty-state, got:\n%s", m.View())
	}
	// tab key cycles to Benchmarks
	nm, _ = m.Update(key("tab"))
	m = nm.(Model)
	if m.active != tabBench {
		t.Fatalf("expected Benchmarks after tab, got %d", m.active)
	}
	if !strings.Contains(m.View(), "no benchmarks") {
		t.Fatalf("benchmarks view should show empty-state, got:\n%s", m.View())
	}
}

func TestQuitKey(t *testing.T) {
	m := testModel()
	_, cmd := m.Update(key("q"))
	if cmd == nil {
		t.Fatal("q should return a command (quit)")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q should return tea.QuitMsg, got %T", cmd())
	}
}

func TestWindowResizeApplied(t *testing.T) {
	m := testModel()
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = nm.(Model)
	if m.width != 120 || m.height != 40 {
		t.Fatalf("resize not applied: %dx%d", m.width, m.height)
	}
}
