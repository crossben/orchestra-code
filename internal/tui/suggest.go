package tui

import (
	"strings"

	"github.com/crossben/orchestra-code/internal/agent"
)

// suggestion is one entry in the chat input's "/" + "@" palette.
type suggestion struct {
	insert string // text the trailing token is replaced with on Tab
	label  string // what is shown in the list
	desc   string
}

// maxSuggest caps how many palette rows are shown at once.
const maxSuggest = 6

// chatCommands lists the slash commands the dashboard's chat understands.
func chatCommands() []suggestion {
	return []suggestion{
		{insert: "/master ", label: "/master", desc: "make an agent the orchestrator (router + default)"},
		{insert: "/route ", label: "/route", desc: "show or toggle AI routing"},
		{insert: "/agents ", label: "/agents", desc: "list agents and availability"},
		{insert: "/help ", label: "/help", desc: "show this command list"},
		{insert: "/parallel ", label: "/parallel", desc: "plan + run independent steps in parallel"},
		{insert: "/par ", label: "/par", desc: "alias of /parallel"},
	}
}

// suggestionsFor returns the palette entries for the trailing token of input:
// "/" commands or "@" agent mentions, filtered by prefix. Empty when the token
// is neither.
func suggestionsFor(input string, reg *agent.Registry) []suggestion {
	// A trailing space means the user is now typing arguments, not a token.
	if strings.TrimRight(input, " \t\n") != input {
		return nil
	}
	tok := trailingToken(input)
	switch {
	case strings.HasPrefix(tok, "/"):
		var out []suggestion
		for _, c := range chatCommands() {
			if strings.HasPrefix(c.label, tok) {
				out = append(out, c)
			}
		}
		return out
	case strings.HasPrefix(tok, "@") && reg != nil:
		prefix := strings.ToLower(tok[1:])
		var out []suggestion
		for _, a := range reg.All() {
			if strings.HasPrefix(strings.ToLower(a.Name()), prefix) {
				out = append(out, suggestion{
					insert: "@" + a.Name() + " ",
					label:  "@" + a.Name(),
					desc:   agentDesc(a),
				})
			}
		}
		return out
	}
	return nil
}

// trailingToken returns the last whitespace-delimited token of s.
func trailingToken(s string) string {
	t := strings.TrimRight(s, " \t\n")
	if i := strings.LastIndexAny(t, " \t\n"); i >= 0 {
		return t[i+1:]
	}
	return t
}

// trailingTokenStart is the index in s where trailingToken(s) begins.
func trailingTokenStart(s string) int {
	t := strings.TrimRight(s, " \t\n")
	if i := strings.LastIndexAny(t, " \t\n"); i >= 0 {
		return i + 1
	}
	return 0
}

// agentDesc renders an agent's capabilities as a short one-line description.
func agentDesc(a agent.Agent) string {
	caps := a.Capabilities()
	if len(caps) == 0 {
		return "agent"
	}
	parts := make([]string, len(caps))
	for i, c := range caps {
		parts[i] = string(c)
	}
	return "agent · " + strings.Join(parts, ", ")
}

// splitMention parses a leading "@agent" mention: it returns the agent name and
// the remaining task text. name is "" when text has no leading mention.
func splitMention(text string) (name, rest string) {
	t := strings.TrimSpace(text)
	if !strings.HasPrefix(t, "@") {
		return "", text
	}
	i := strings.IndexAny(t, " \t\n")
	if i < 0 {
		return t[1:], ""
	}
	return t[1:i], strings.TrimSpace(t[i+1:])
}

// refreshSuggestions recomputes the palette from the current input. It is a
// no-op unless the Chat tab is idle, and re-lays out so the transcript shrinks
// by exactly the palette's height.
func (m *Model) refreshSuggestions() {
	if m.active != tabChat || m.cstate != chatIdle {
		m.sugg, m.suggSel = nil, 0
		return
	}
	m.sugg = suggestionsFor(m.ta.Value(), m.d.Reg)
	if m.suggSel >= len(m.sugg) {
		m.suggSel = 0
	}
	m.layout()
}

// completeSuggestion replaces the trailing token with the selected entry.
func (m *Model) completeSuggestion() {
	if m.suggSel < 0 || m.suggSel >= len(m.sugg) {
		return
	}
	text := m.ta.Value()
	newText := text[:trailingTokenStart(text)] + m.sugg[m.suggSel].insert
	m.ta.SetValue(newText)
	m.ta.CursorEnd()
	m.refreshSuggestions()
}

// suggHeight is how many rows the palette needs (0 when it is closed).
func (m Model) suggHeight() int {
	if m.cstate != chatIdle {
		return 0
	}
	return min(len(m.sugg), maxSuggest)
}

// renderSuggestions draws the palette to width w.
func (m Model) renderSuggestions(w int) string {
	if len(m.sugg) == 0 {
		return ""
	}
	lines := make([]string, 0, len(m.sugg))
	for i, s := range m.sugg {
		if i >= maxSuggest {
			break
		}
		label := fit(s.label, 16)
		if i == m.suggSel {
			lines = append(lines, fit("  "+selSty.Render("▸ "+label)+" "+dimSty.Render(s.desc), w))
		} else {
			lines = append(lines, fit("    "+keySty.Render(label)+" "+dimSty.Render(s.desc), w))
		}
	}
	return strings.Join(lines, "\n")
}
