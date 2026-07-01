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
	"io"
	"os"
	"strings"
	"time"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/engine"
	"github.com/crossben/orchestra/internal/memory"
	"github.com/crossben/orchestra/internal/router"
	"github.com/crossben/orchestra/internal/validate"
)

// Shell holds interactive-session state.
type Shell struct {
	reg        *agent.Registry
	dir        string
	stages     []validate.Stage
	maxRetries int
	timeout    time.Duration
	current    string // active agent when routing is off / for @-less manual mode
	routing    bool   // AI routing on/off
	router     *router.Router
	mem        *memory.Store
	in         *bufio.Reader
}

// New builds a Shell. If rtr is non-nil and routingOn is true, messages are
// auto-routed; otherwise the shell uses the fixed active agent.
func New(reg *agent.Registry, dir, defaultAgent string, stages []validate.Stage, maxRetries int, timeout time.Duration, mem *memory.Store, rtr *router.Router, routingOn bool) *Shell {
	return &Shell{
		reg:        reg,
		dir:        dir,
		stages:     stages,
		maxRetries: maxRetries,
		timeout:    timeout,
		current:    defaultAgent,
		routing:    routingOn && rtr != nil,
		router:     rtr,
		mem:        mem,
		in:         bufio.NewReader(os.Stdin),
	}
}

// Run drives the read-eval loop until EOF or /exit.
func (s *Shell) Run(ctx context.Context) error {
	s.banner()
	for {
		fmt.Printf("\norchestra (%s) › ", s.promptTag())
		line, err := s.in.ReadString('\n')
		if err == io.EOF {
			fmt.Println("\nbye 👋")
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			if s.command(line) { // returns true to exit
				return nil
			}
			continue
		}
		s.handle(ctx, line)
	}
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
	fmt.Println("  … routing")
	d := s.router.Route(ctx, line, s.dir)
	if d.IsQuestion() {
		ans, err := s.router.Answer(ctx, line, s.dir, s.timeout)
		if err != nil {
			fmt.Printf("  error answering: %v\n", err)
			return
		}
		fmt.Println(strings.TrimSpace(ans))
		return
	}
	fmt.Printf("  ↳ %s → agent %q (%s)\n", d.Intent, d.Agent, d.Reason)
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
	_, err := engine.Execute(ctx, s.in, engine.Options{
		Agent:          ag,
		Prompt:         msg,
		Dir:            s.dir,
		Stages:         s.stages,
		MaxRetries:     s.maxRetries,
		Timeout:        s.timeout,
		CommitOnAccept: true,
		Memory:         s.mem,
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
		return "auto"
	}
	return s.current
}

func (s *Shell) banner() {
	fmt.Println("orchestra — interactive session (M4)")
	mode := "AI routing on — just type; @name or /route off to override"
	if !s.routing {
		mode = fmt.Sprintf("manual mode — active agent %q; /route on to auto-route", s.current)
	}
	fmt.Println(mode)
	fmt.Println("/help for commands, /exit to quit.")
	s.listAgents()
}

func (s *Shell) help() {
	fmt.Print(`  commands:
    /agents          list agents and availability
    /route [on|off]  show or toggle AI routing
    /agent <name>    set the active agent (used when routing is off)
    /help            show this help
    /exit            leave the session
  messages:
    <text>           routed automatically (or sent to the active agent if routing is off)
    @<name> <task>   force this one task to a specific agent
`)
}

func (s *Shell) listAgents() {
	fmt.Println("  agents:")
	for _, a := range s.reg.All() {
		status := "✓"
		if err := a.Health(); err != nil {
			status = "✗ not installed"
		}
		marker := "  "
		if !s.routing && a.Name() == s.current {
			marker = "▸ "
		}
		fmt.Printf("    %s%-10s %s\n", marker, a.Name(), status)
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
