package tui

import (
	"context"
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
	return New(Deps{Ctx: context.Background(), Cfg: cfg, Reg: reg, Mem: nil, Dir: ".", DefaultAgent: "alpha"})
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

func TestChatTabTypingAndBackspace(t *testing.T) {
	m := testModel()
	m.active = tabChat
	// type "hi"
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = nm.(Model)
	if m.input != "hi x" {
		t.Fatalf("expected input %q, got %q", "hi x", m.input)
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = nm.(Model)
	if m.input != "hi " {
		t.Fatalf("backspace failed, got %q", m.input)
	}
	if !strings.Contains(m.View(), "Chat") {
		t.Fatal("chat view should render")
	}
}

func TestChatEscLeavesTab(t *testing.T) {
	m := testModel()
	m.active = tabChat
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(Model)
	if m.active != tabAgents {
		t.Fatalf("esc should leave chat to Agents, got %d", m.active)
	}
}

func TestChatEmptySubmitNoop(t *testing.T) {
	m := testModel()
	m.active = tabChat
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // empty input
	if cmd != nil {
		t.Fatal("submitting empty input should not launch a command")
	}
}

func TestChatTabInNav(t *testing.T) {
	m := testModel()
	nm, _ := m.Update(key("4"))
	m = nm.(Model)
	if m.active != tabChat {
		t.Fatalf("key 4 should select Chat, got %d", m.active)
	}
}
