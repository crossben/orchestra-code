package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/router"
)

func suggestRegistry() *agent.Registry {
	reg := agent.NewRegistry()
	reg.Add(agent.New("alpha", "true", nil, "", []agent.Capability{agent.CapImplement}))
	reg.Add(agent.New("beta", "true", nil, "", []agent.Capability{agent.CapReview}))
	return reg
}

func TestSuggestionsFor(t *testing.T) {
	reg := suggestRegistry()
	cases := []struct {
		in   string
		want int
	}{
		{"/par", 2},       // /parallel and /par
		{"/parallel ", 0}, // already has args → palette hidden
		{"@", 2},          // both agents
		{"@al", 1},        // alpha only
		{"hello", 0},      // not a command or mention
		{"add @be", 1},    // mention mid-message
	}
	for _, tc := range cases {
		if got := suggestionsFor(tc.in, reg); len(got) != tc.want {
			t.Fatalf("suggestionsFor(%q) = %d items, want %d", tc.in, len(got), tc.want)
		}
	}
	if got := suggestionsFor("@al", reg); len(got) == 1 && got[0].insert != "@alpha " {
		t.Fatalf("insert = %q, want %q", got[0].insert, "@alpha ")
	}
}

func TestChatPaletteTabCompletes(t *testing.T) {
	m := testModel()
	m.ta.SetValue("/par")
	m.refreshSuggestions()
	if len(m.sugg) == 0 {
		t.Fatal("expected suggestions for /par")
	}
	nm, _ := m.onKey(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if m.ta.Value() != "/parallel " {
		t.Fatalf("after Tab, value = %q, want %q", m.ta.Value(), "/parallel ")
	}
}

func TestChatPaletteAgentCompletion(t *testing.T) {
	m := testModel() // registry has alpha, beta
	m.ta.SetValue("please @be")
	m.refreshSuggestions()
	if len(m.sugg) != 1 || m.sugg[0].label != "@beta" {
		t.Fatalf("suggestions = %+v, want [@beta]", m.sugg)
	}
	nm, _ := m.onKey(tea.KeyMsg{Type: tea.KeyTab})
	m = nm.(Model)
	if m.ta.Value() != "please @beta " {
		t.Fatalf("after Tab, value = %q, want %q", m.ta.Value(), "please @beta ")
	}
}

func TestChatViewShowsPalette(t *testing.T) {
	m := testModel()
	m.ta.SetValue("/par")
	m.refreshSuggestions()
	if out := m.View(); !strings.Contains(out, "/parallel") {
		t.Fatalf("chat view should show the palette, got:\n%s", out)
	}
}

func TestMasterPrefix(t *testing.T) {
	cases := []struct {
		in  string
		arg string
		ok  bool
	}{
		{"/master", "", true},
		{"/master beta", "beta", true},
		{"/master  beta ", "beta", true},
		{"/masterx", "", false},
		{"/parallel x", "", false},
		{"do it", "", false},
	}
	for _, c := range cases {
		arg, ok := masterPrefix(c.in)
		if arg != c.arg || ok != c.ok {
			t.Fatalf("masterPrefix(%q) = (%q, %v), want (%q, %v)", c.in, arg, ok, c.arg, c.ok)
		}
	}
}

func TestDashboardMasterCommand(t *testing.T) {
	reg := suggestRegistry()
	alpha, _ := reg.Get("alpha")
	r := router.New(nil, alpha.(agent.Querier), reg, nil, "alpha", "alpha", time.Second)

	m := testModelIn(".", reg, nil, 100, 30)
	m.d.Router = r
	m.d.RoutingOn = true
	m.master = "alpha"

	m.ta.SetValue("/master beta")
	nm, _ := m.submitChat()
	m = nm.(Model)

	if m.master != "beta" {
		t.Fatalf("model master = %q, want %q", m.master, "beta")
	}
	if m.d.DefaultAgent != "beta" {
		t.Fatalf("default agent = %q, want %q", m.d.DefaultAgent, "beta")
	}
	if r.MasterAgent() != "beta" {
		t.Fatalf("router master = %q, want %q", r.MasterAgent(), "beta")
	}
}

func TestRoutePrefix(t *testing.T) {
	cases := []struct {
		in  string
		arg string
		ok  bool
	}{
		{"/route", "", true},
		{"/route off", "off", true},
		{"/route  on ", "on", true},
		{"/routing", "", false},
		{"/master x", "", false},
	}
	for _, c := range cases {
		arg, ok := routePrefix(c.in)
		if arg != c.arg || ok != c.ok {
			t.Fatalf("routePrefix(%q) = (%q, %v), want (%q, %v)", c.in, arg, ok, c.arg, c.ok)
		}
	}
}

func TestDashboardRouteCommand(t *testing.T) {
	reg := suggestRegistry()
	alpha, _ := reg.Get("alpha")
	r := router.New(nil, alpha.(agent.Querier), reg, nil, "alpha", "alpha", time.Second)

	m := testModelIn(".", reg, nil, 100, 30)
	m.d.Router = r
	m.d.RoutingOn = true

	m.ta.SetValue("/route off")
	nm, _ := m.submitChat()
	m = nm.(Model)
	if m.d.RoutingOn {
		t.Fatal("/route off should disable routing")
	}

	m.ta.SetValue("/route on")
	nm, _ = m.submitChat()
	m = nm.(Model)
	if !m.d.RoutingOn {
		t.Fatal("/route on should enable routing")
	}
}

func TestDashboardAgentsCommand(t *testing.T) {
	m := testModel() // registry has alpha, beta
	m.ta.SetValue("/agents")
	nm, _ := m.submitChat()
	m = nm.(Model)

	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.text, "agents:") || !strings.Contains(last.text, "alpha") {
		t.Fatalf("agents output = %q", last.text)
	}
}

func TestDashboardHelpCommand(t *testing.T) {
	m := testModel()
	m.ta.SetValue("/help")
	nm, _ := m.submitChat()
	m = nm.(Model)

	last := m.messages[len(m.messages)-1]
	if !strings.Contains(last.text, "commands:") || !strings.Contains(last.text, "/master") {
		t.Fatalf("help output = %q", last.text)
	}
}

func TestHeaderShowsMaster(t *testing.T) {
	reg := suggestRegistry()
	alpha, _ := reg.Get("alpha")
	r := router.New(nil, alpha.(agent.Querier), reg, nil, "alpha", "alpha", time.Second)

	m := testModelIn(".", reg, nil, 160, 30)
	m.d.Router = r
	m.d.RoutingOn = true
	m.master = "beta"

	if out := m.header(); !strings.Contains(out, "beta") {
		t.Fatalf("header should show the master, got:\n%s", out)
	}
}

func TestSplitMention(t *testing.T) {
	cases := []struct{ in, name, rest string }{
		{"@claude add tests", "claude", "add tests"},
		{"@claude", "claude", ""},
		{"add tests", "", "add tests"},
		{"  @mimo do it  ", "mimo", "do it"},
	}
	for _, c := range cases {
		name, rest := splitMention(c.in)
		if name != c.name || rest != c.rest {
			t.Fatalf("splitMention(%q) = (%q, %q), want (%q, %q)", c.in, name, rest, c.name, c.rest)
		}
	}
}
