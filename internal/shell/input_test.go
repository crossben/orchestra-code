package shell

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/router"
)

func TestFilterCommands(t *testing.T) {
	cmds := shellCommands()
	cases := []struct {
		in   string
		want int
	}{
		{"/", len(cmds)},
		{"/ma", 1},
		{"/master ", 0}, // already has args → palette hidden
		{"hello", 0},    // not a command
		{"/zzz", 0},     // no match
	}
	for _, tc := range cases {
		if got := filterCommands(cmds, tc.in); len(got) != tc.want {
			t.Fatalf("filterCommands(%q) = %d items, want %d", tc.in, len(got), tc.want)
		}
	}
	if got := filterCommands(cmds, "/ma"); len(got) != 1 || got[0].name != "/master" {
		t.Fatalf("filterCommands(/ma) = %+v, want [/master]", got)
	}
}

// typing drives the model through a sequence of plain rune keystrokes.
func (m inputModel) typing(s string) inputModel {
	for _, r := range s {
		nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = nm.(inputModel)
	}
	return m
}

func TestInputModelPalette(t *testing.T) {
	m := newInputModel("orchestra › ", shellCommands())

	m = m.typing("/ma")
	if len(m.matches) != 1 || m.matches[0].name != "/master" {
		t.Fatalf("matches = %+v, want [/master]", m.matches)
	}

	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(inputModel)
	if m.ti.Value() != "/master " {
		t.Fatalf("after Tab, value = %q, want %q", m.ti.Value(), "/master ")
	}

	nm, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = nm.(inputModel)
	if !m.done || m.canceled {
		t.Fatalf("Enter should submit: done=%v canceled=%v", m.done, m.canceled)
	}
	if m.value != "/master " {
		t.Fatalf("submitted value = %q, want %q", m.value, "/master ")
	}
}

func TestFilterAgentMentions(t *testing.T) {
	sug := []command{
		{name: "/master", args: "<agent>"},
		{name: "@claude", args: "<task>"},
		{name: "@opencode", args: "<task>"},
	}
	if got := filterCommands(sug, "/"); len(got) != 1 || got[0].name != "/master" {
		t.Fatalf("filterCommands(/) = %+v, want [/master]", got)
	}
	if got := filterCommands(sug, "@"); len(got) != 2 {
		t.Fatalf("filterCommands(@) = %d items, want 2", len(got))
	}
	if got := filterCommands(sug, "@cl"); len(got) != 1 || got[0].name != "@claude" {
		t.Fatalf("filterCommands(@cl) = %+v, want [@claude]", got)
	}
}

func TestInputModelAgentCompletion(t *testing.T) {
	m := newInputModel("› ", []command{
		{name: "@claude", args: "<task>"},
		{name: "@opencode", args: "<task>"},
	})
	m = m.typing("@cl")
	if len(m.matches) != 1 || m.matches[0].name != "@claude" {
		t.Fatalf("matches = %+v, want [@claude]", m.matches)
	}
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(inputModel)
	if m.ti.Value() != "@claude " {
		t.Fatalf("after Tab, value = %q, want %q", m.ti.Value(), "@claude ")
	}
}

func TestInputModelCancel(t *testing.T) {
	m := newInputModel("orchestra › ", shellCommands())
	nm, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = nm.(inputModel)
	if !m.done || !m.canceled {
		t.Fatalf("Esc should cancel: done=%v canceled=%v", m.done, m.canceled)
	}
}

func TestMasterCommandSwitches(t *testing.T) {
	reg := agent.NewRegistry()
	reg.Add(agent.New("alpha", "true", nil, "", nil))
	reg.Add(agent.New("beta", "true", nil, "", nil))

	alpha, _ := reg.Get("alpha")
	r := router.New(nil, alpha.(agent.Querier), reg, nil, "alpha", "alpha", time.Second)
	s := &Shell{reg: reg, router: r, current: "alpha", master: "alpha", routing: true}

	s.command("/master beta")
	if s.master != "beta" {
		t.Fatalf("shell master = %q, want %q", s.master, "beta")
	}
	if r.MasterAgent() != "beta" {
		t.Fatalf("router master = %q, want %q", r.MasterAgent(), "beta")
	}

	s.command("/master nope")
	if s.master != "beta" {
		t.Fatalf("unknown agent should not change master; got %q", s.master)
	}
}
