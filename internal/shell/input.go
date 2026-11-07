package shell

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/crossben/orchestra-code/internal/ui"
)

// command is one shell command: its name, its argument hint (may be empty) and
// a one-line description. The single list (shellCommands) feeds both /help and
// the "/" palette.
type command struct {
	name string // e.g. "/master"
	args string // e.g. "<agent>" (may be empty)
	desc string
}

// label renders the name plus its argument hint, e.g. "/master <agent>".
func (c command) label() string {
	if c.args == "" {
		return c.name
	}
	return c.name + " " + c.args
}

// maxMatches caps how many palette rows are shown at once.
const maxMatches = 6

// inputModel is a single-line Bubble Tea prompt. When the line starts with "/"
// it shows a filtered command palette (opencode-style); Tab completes the
// highlighted command. It returns exactly one line and then quits.
type inputModel struct {
	ti       textinput.Model
	commands []command
	matches  []command
	sel      int
	width    int
	value    string
	canceled bool
	done     bool
}

// newInputModel builds the input prompt with the given plain-text prompt string.
func newInputModel(prompt string, cmds []command) inputModel {
	ti := textinput.New()
	ti.Prompt = prompt
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true)
	ti.Placeholder = "Describe a change, ask a question, or type / for commands…"
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
	ti.CharLimit = 0
	ti.Focus()
	return inputModel{ti: ti, commands: cmds}
}

// filterCommands returns the suggestions to show for the current input: every
// entry whose name has input as a prefix. It only shows a palette while the
// user is typing a bare "/" command or "@" agent mention (no space yet).
func filterCommands(cmds []command, input string) []command {
	if strings.ContainsAny(input, " \t") {
		return nil
	}
	if !strings.HasPrefix(input, "/") && !strings.HasPrefix(input, "@") {
		return nil
	}
	prefix := strings.ToLower(input)
	var out []command
	for _, c := range cmds {
		if strings.HasPrefix(strings.ToLower(c.name), prefix) {
			out = append(out, c)
		}
	}
	return out
}

// refresh recomputes matches from the current input and clamps the selection.
func (m *inputModel) refresh() {
	m.matches = filterCommands(m.commands, m.ti.Value())
	if m.sel >= len(m.matches) {
		m.sel = len(m.matches) - 1
	}
	if m.sel < 0 {
		m.sel = 0
	}
}

// completeHiLighted fills the input with the highlighted command (plus a space
// when it takes arguments).
func (m *inputModel) completeHighlighted() {
	if len(m.matches) == 0 {
		return
	}
	c := m.matches[m.sel]
	v := c.name
	if c.args != "" {
		v += " "
	}
	m.ti.SetValue(v)
	m.ti.SetCursor(len(v))
	m.refresh()
}

func (m inputModel) Init() tea.Cmd { return textinput.Blink }

func (m inputModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.canceled = true
			m.done = true
			return m, tea.Quit
		case tea.KeyEnter:
			m.value = m.ti.Value()
			m.done = true
			m.matches = nil // don't leave the palette on screen after submit
			return m, tea.Quit
		case tea.KeyTab:
			if len(m.matches) > 0 {
				m.completeHighlighted()
			}
			return m, nil
		case tea.KeyUp, tea.KeyCtrlP:
			if m.sel > 0 {
				m.sel--
			}
			return m, nil
		case tea.KeyDown, tea.KeyCtrlN:
			if m.sel < len(m.matches)-1 {
				m.sel++
			}
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.ti, cmd = m.ti.Update(msg)
	m.refresh()
	return m, cmd
}

func (m inputModel) View() string {
	var b strings.Builder
	b.WriteString(m.ti.View())
	if len(m.matches) == 0 {
		return b.String()
	}
	b.WriteByte('\n')
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#06B6D4")).Bold(true)
	for i, c := range m.matches {
		if i >= maxMatches {
			break
		}
		label := fmt.Sprintf("%-20s", c.label())
		if i == m.sel {
			b.WriteString("  " + sel.Render("▸ "+label) + " " + ui.Dim(c.desc))
		} else {
			b.WriteString("    " + ui.Accent2(label) + " " + ui.Dim(c.desc))
		}
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n")
}
