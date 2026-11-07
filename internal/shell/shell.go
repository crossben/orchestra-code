// Package shell is Orchestra's interactive chat interface — the primary UX. You
// stay in one session and type; the AI router reads each message and either
// answers a plain question directly or dispatches a coding task to the best
// agent (M4). You can still force an agent with @name, or turn routing off with
// /route off to fall back to a fixed active agent.
package shell

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/router"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/validate"
)

// Shell holds interactive-session state.
type Shell struct {
	reg        *agent.Registry
	dir        string
	stages     []validate.Stage
	maxRetries int
	timeout    time.Duration
	current    string // active agent when routing is off / for @-less manual mode
	master     string // orchestrator agent (classifies + answers + default dispatch)
	routing    bool   // AI routing on/off
	router     *router.Router
	mem        *memory.Store
	principles string // lean-code preamble text (may be empty)
	in         *bufio.Reader
}

// New builds a Shell. If rtr is non-nil and routingOn is true, messages are
// auto-routed; otherwise the shell uses the fixed active agent.
func New(reg *agent.Registry, dir, defaultAgent string, stages []validate.Stage, maxRetries int, timeout time.Duration, mem *memory.Store, rtr *router.Router, routingOn bool, principles string) *Shell {
	master := defaultAgent
	if rtr != nil && rtr.MasterAgent() != "" {
		master = rtr.MasterAgent()
	}
	return &Shell{
		reg:        reg,
		dir:        dir,
		stages:     stages,
		maxRetries: maxRetries,
		timeout:    timeout,
		current:    defaultAgent,
		master:     master,
		routing:    routingOn && rtr != nil,
		router:     rtr,
		mem:        mem,
		principles: principles,
		in:         bufio.NewReader(os.Stdin),
	}
}

// Run drives the read-eval loop until EOF, /exit, or context cancellation (Ctrl+C).
func (s *Shell) Run(ctx context.Context) error {
	s.banner()
	for {
		line, ok := s.readLine(ctx)
		if !ok {
			fmt.Println("\nbye 👋")
			return nil
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if s.command(line) {
				return nil
			}
			continue
		}
		s.handle(ctx, line)
	}
}

// readLine reads one line of input. In an interactive terminal it runs a Bubble
// Tea prompt with a "/" command palette; otherwise it reads from stdin directly
// (pipes, CI, tests) so scripted use is never affected.
func (s *Shell) readLine(ctx context.Context) (string, bool) {
	if !interactive() {
		line, err := s.in.ReadString('\n')
		if err != nil { // EOF or read error → end the session
			return "", false
		}
		return line, true
	}
	p := tea.NewProgram(newInputModel(s.promptString(), s.suggestions()), tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		return "", false
	}
	im := final.(inputModel)
	if im.canceled {
		return "", false
	}
	return im.value, true
}

// promptString is the plain-text prompt shown by the Bubble Tea input (styling
// is applied via textinput.PromptStyle, so ANSI never confuses width math).
func (s *Shell) promptString() string {
	return fmt.Sprintf("orchestra (%s) › ", s.promptTag())
}

// interactive reports whether both stdin and stdout are terminals. When they are
// not (piped, CI, tests), the shell avoids raw-mode input entirely.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// handle decides what to do with a message: honor an @agent override, else route
// (when routing is on), else send to the active agent.
func (s *Shell) handle(ctx context.Context, line string) {
	// @agent override always wins and bypasses the router.
	if strings.HasPrefix(line, "@") {
		name, rest, _ := strings.Cut(line[1:], " ")
		msg := strings.TrimSpace(rest)
		if msg == "" {
			fmt.Printf("  (no task after @%s)\n", name)
			return
		}
		s.runTask(ctx, name, msg)
		return
	}

	if !s.routing {
		s.runTask(ctx, s.current, line)
		return
	}

	// AI routing.
	sp := ui.Spin("routing…")
	d := s.router.Route(ctx, line, s.dir)
	sp.Stop()
	if d.IsQuestion() {
		sp := ui.Spin("thinking…")
		ans, err := s.router.Answer(ctx, line, s.dir, s.timeout)
		sp.Stop()
		if err != nil {
			fmt.Printf("  %s %v\n", ui.Danger("error answering:"), err)
			return
		}
		fmt.Println(strings.TrimSpace(ans))
		return
	}
	fmt.Printf("  %s %s → %s %s\n", ui.Accent2("↳"), d.Intent, ui.Agent(d.Agent), ui.Dim("("+d.Reason+")"))
	s.runTask(ctx, d.Agent, line)
}

// runTask dispatches a coding task to a named agent through the engine.
func (s *Shell) runTask(ctx context.Context, agentName, msg string) {
	ag, ok := s.reg.Get(agentName)
	if !ok {
		fmt.Printf("  unknown agent %q — try /agents\n", agentName)
		return
	}
	if err := ag.Health(); err != nil {
		fmt.Printf("  agent %q is not available: %v\n", agentName, err)
		return
	}
	// In interactive terminals the Bubble Tea prompt owns stdin, so give the
	// engine's accept/reject prompt a fresh reader over the restored terminal.
	in := s.in
	if interactive() {
		in = bufio.NewReader(os.Stdin)
	}
	_, err := engine.Execute(ctx, in, engine.Options{
		Agent:          ag,
		Prompt:         msg,
		Dir:            s.dir,
		Stages:         s.stages,
		MaxRetries:     s.maxRetries,
		Timeout:        s.timeout,
		CommitOnAccept: true,
		Memory:         s.mem,
		Principles:     s.principles,
	})
	if err != nil {
		fmt.Printf("  error: %v\n", err)
	}
}

// command handles /-prefixed shell commands. Returns true if the shell should
// exit.
func (s *Shell) command(line string) bool {
	name, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	switch name {
	case "/exit", "/quit", "/q":
		fmt.Println("bye 👋")
		return true
	case "/help", "/h":
		s.help()
	case "/agents":
		s.listAgents()
	case "/master":
		s.masterCmd(arg)
	case "/route":
		s.routeCmd(arg)
	case "/agent":
		if arg == "" {
			fmt.Printf("  active agent: %s\n", s.current)
			return false
		}
		if _, ok := s.reg.Get(arg); !ok {
			fmt.Printf("  unknown agent %q — try /agents\n", arg)
			return false
		}
		s.current = arg
		if s.routing {
			fmt.Printf("  active agent set to %s (routing is on; use /route off to use it)\n", arg)
		} else {
			fmt.Printf("  switched to %s\n", arg)
		}
	default:
		fmt.Printf("  unknown command %q — try /help\n", name)
	}
	return false
}

// masterCmd shows or switches the orchestrator agent — the agent that
// classifies intent, answers plain questions, and is the default dispatch agent.
func (s *Shell) masterCmd(arg string) {
	if arg == "" {
		master := s.master
		if s.router != nil {
			master = s.router.MasterAgent()
		}
		fmt.Printf("  master: %s (routing %s)\n", ui.Agent(master), onOff(s.routing))
		return
	}
	ag, ok := s.reg.Get(arg)
	if !ok {
		fmt.Printf("  unknown agent %q — try /agents\n", arg)
		return
	}
	if s.router == nil {
		fmt.Println("  routing unavailable (no router configured) — /master has no effect")
		return
	}
	if err := ag.Health(); err != nil {
		fmt.Printf("  agent %q is not available: %v\n", arg, err)
		return
	}
	if err := s.router.SetMaster(ag); err != nil {
		fmt.Printf("  cannot make %q master: %v\n", arg, err)
		return
	}
	s.master = arg
	fmt.Printf("  master set to %s — it now classifies, answers, and is the default agent\n", ui.Agent(arg))
}

// routeCmd toggles or reports AI routing.
func (s *Shell) routeCmd(arg string) {
	switch strings.ToLower(arg) {
	case "":
		fmt.Printf("  routing is %s\n", onOff(s.routing))
	case "on":
		if s.router == nil {
			fmt.Println("  routing unavailable (no router configured)")
			return
		}
		s.routing = true
		fmt.Println("  routing on — messages are auto-dispatched")
	case "off":
		s.routing = false
		fmt.Printf("  routing off — using active agent %q\n", s.current)
	default:
		fmt.Println("  usage: /route [on|off]")
	}
}

func (s *Shell) promptTag() string {
	if s.routing {
		return "auto · " + s.master
	}
	return s.current
}

func (s *Shell) banner() {
	ui.Banner("the operating system for AI coding agents")
	fmt.Println()
	mode := ui.Success("● ") + fmt.Sprintf("AI routing on — master %s; ", ui.Agent(s.master)) + ui.Accent2("@name") + " or " + ui.Accent2("/route off") + " to override"
	if !s.routing {
		mode = ui.Dim("○ ") + fmt.Sprintf("manual mode — active agent %s; %s to auto-route", ui.Agent(s.current), ui.Accent2("/route on"))
	}
	fmt.Println(mode)
	fmt.Println(ui.Dim("type / for commands, /help for help, /exit to quit."))
	s.listAgents()
}

// shellCommands is the single source of truth for the shell's commands — it
// drives both /help and the "/" palette.
func shellCommands() []command {
	return []command{
		{"/master", "<agent>", "make an agent the orchestrator (router + default)"},
		{"/agents", "", "list agents and availability"},
		{"/route", "[on|off]", "show or toggle AI routing"},
		{"/agent", "<name>", "set the active agent (used when routing is off)"},
		{"/help", "", "show this help"},
		{"/exit", "", "leave the session"},
	}
}

// suggestions is the shell palette's full list: the "/" commands plus an
// "@<agent>" mention for every registered agent (so @-completion works).
func (s *Shell) suggestions() []command {
	out := shellCommands()
	for _, a := range s.reg.All() {
		out = append(out, command{name: "@" + a.Name(), args: "<task>", desc: capDesc(a)})
	}
	return out
}

// capDesc renders an agent's capabilities as a short one-line description.
func capDesc(a agent.Agent) string {
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

func (s *Shell) help() {
	fmt.Println("  commands:")
	for _, c := range shellCommands() {
		fmt.Printf("    %-20s %s\n", c.label(), c.desc)
	}
	fmt.Print(`  messages:
    <text>           routed automatically (or sent to the active agent if routing is off)
    @<name> <task>   force this one task to a specific agent
`)
}

func (s *Shell) listAgents() {
	fmt.Println(ui.Dim("  agents:"))
	for _, a := range s.reg.All() {
		status := ui.Success("✓")
		if err := a.Health(); err != nil {
			status = ui.Dim("✗ not installed")
		}
		marker := "  "
		switch {
		case s.routing && a.Name() == s.master:
			marker = ui.Accent("★ ")
		case !s.routing && a.Name() == s.current:
			marker = ui.Accent2("▸ ")
		}
		fmt.Printf("    %s%s %s\n", marker, ui.Agent(fmt.Sprintf("%-10s", a.Name())), status)
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
