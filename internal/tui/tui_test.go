package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/config"
	"github.com/crossben/orchestra/internal/engine"
)

func testModel() Model {
	reg := agent.NewRegistry()
	reg.Add(agent.New("alpha", "true", nil, "", []agent.Capability{agent.CapImplement}))
	reg.Add(agent.New("beta", "true", nil, "", []agent.Capability{agent.CapReview}))
	cfg := &config.Config{DefaultAgent: "alpha"}
	m := New(Deps{Ctx: context.Background(), Cfg: cfg, Reg: reg, Mem: nil, Dir: ".", DefaultAgent: "alpha"})
	nm, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30}) // make it ready + size viewport/input
	return nm.(Model)
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
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hi")})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = nm.(Model)
	if m.ti.Value() != "hi x" {
		t.Fatalf("expected input %q, got %q", "hi x", m.ti.Value())
	}
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	m = nm.(Model)
	if m.ti.Value() != "hi " {
		t.Fatalf("backspace failed, got %q", m.ti.Value())
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

// tab must navigate away even while typing (the reported bug).
func TestChatTabNavigatesWhileTyping(t *testing.T) {
	m := testModel()
	m.active = tabChat
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("hello")})
	m = nm.(Model)
	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if m.active == tabChat {
		t.Fatal("tab should navigate out of chat while typing")
	}
	if m.ti.Value() != "hello" {
		t.Fatalf("input should be preserved on nav, got %q", m.ti.Value())
	}
}

func TestChatSubmitEntersRunning(t *testing.T) {
	m := testModel()
	m.active = tabChat
	m.ti.SetValue("do a thing")
	nm, cmd := m.submitChat()
	m = nm.(Model)
	if m.cstate != chatRunning {
		t.Fatalf("expected running state, got %d", m.cstate)
	}
	if cmd == nil {
		t.Fatal("submit should return a command (spinner+produce)")
	}
	if len(m.messages) != 1 || m.messages[0].role != "you" {
		t.Fatalf("user message not recorded: %+v", m.messages)
	}
}

func TestOnTurnStates(t *testing.T) {
	// no changes → idle + agent message
	m := testModel()
	m.cstate = chatRunning
	nm, _ := m.onTurn(engine.Turn{HadChanges: false, AgentText: "here is an explanation"}, "")
	m = nm.(Model)
	if m.cstate != chatIdle || m.messages[len(m.messages)-1].role != "agent" {
		t.Fatalf("no-change turn should go idle with agent msg, got state=%d", m.cstate)
	}
	// changes → reviewing
	m2 := testModel()
	m2.cstate = chatRunning
	nm2, _ := m2.onTurn(engine.Turn{HadChanges: true, Diff: "diff --git a b\n+x"}, "")
	m2 = nm2.(Model)
	if m2.cstate != chatReviewing {
		t.Fatalf("changed turn should enter reviewing, got %d", m2.cstate)
	}
	// error → idle + sys message
	m3 := testModel()
	m3.cstate = chatRunning
	nm3, _ := m3.onTurn(engine.Turn{Err: errDirty}, "")
	m3 = nm3.(Model)
	if m3.cstate != chatIdle || m3.messages[len(m3.messages)-1].role != "sys" {
		t.Fatal("error turn should go idle with sys msg")
	}
}

func TestRenderMarkdown(t *testing.T) {
	out := renderMarkdown("# Title\n\nSome **bold** text and `code`.\n\n- item one\n- item two", 60)
	if out == "" {
		t.Fatal("markdown render returned empty")
	}
	if strings.Contains(out, "**bold**") {
		t.Fatalf("markdown not rendered (raw ** present):\n%s", out)
	}
}
