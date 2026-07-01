// Package shell is Orchestra's interactive chat interface — the primary UX. You
// stay in one session and type tasks; each message runs through the same engine
// as `orchestra run`. Agent selection is manual for now (a default you can
// switch, or a per-message @agent prefix); the AI router (M4) will make the
// choice automatically.
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
)

// Shell holds interactive-session state.
type Shell struct {
	reg     *agent.Registry
	dir     string
	testCmd string
	timeout time.Duration
	current string // current default agent
	in      *bufio.Reader
}

// New builds a Shell. defaultAgent selects the initially active agent.
func New(reg *agent.Registry, dir, defaultAgent, testCmd string, timeout time.Duration) *Shell {
	return &Shell{
		reg:     reg,
		dir:     dir,
		testCmd: testCmd,
		timeout: timeout,
		current: defaultAgent,
		in:      bufio.NewReader(os.Stdin),
	}
}

// Run drives the read-eval loop until EOF or /exit.
func (s *Shell) Run(ctx context.Context) error {
	s.banner()
	for {
		fmt.Printf("\norchestra (%s) › ", s.current)
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
		s.dispatch(ctx, line)
	}
}

// dispatch resolves the target agent (honoring a leading @agent) and runs the
// message through the engine.
func (s *Shell) dispatch(ctx context.Context, line string) {
	target := s.current
	msg := line
	if strings.HasPrefix(line, "@") {
		name, rest, _ := strings.Cut(line[1:], " ")
		target = name
		msg = strings.TrimSpace(rest)
		if msg == "" {
			fmt.Printf("  (no task after @%s)\n", target)
			return
		}
	}

	ag, ok := s.reg.Get(target)
	if !ok {
		fmt.Printf("  unknown agent %q — try /agents\n", target)
		return
	}
	if err := ag.Health(); err != nil {
		fmt.Printf("  agent %q is not available: %v\n", target, err)
		return
	}

	_, err := engine.Execute(ctx, s.in, engine.Options{
		Agent:          ag,
		Prompt:         msg,
		Dir:            s.dir,
		TestCommand:    s.testCmd,
		Timeout:        s.timeout,
		CommitOnAccept: true,
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
	case "/agent":
		if arg == "" {
			fmt.Printf("  current agent: %s\n", s.current)
			return false
		}
		if _, ok := s.reg.Get(arg); !ok {
			fmt.Printf("  unknown agent %q — try /agents\n", arg)
			return false
		}
		s.current = arg
		fmt.Printf("  switched to %s\n", arg)
	default:
		fmt.Printf("  unknown command %q — try /help\n", name)
	}
	return false
}

func (s *Shell) banner() {
	fmt.Println("orchestra — interactive session (M1)")
	fmt.Println("type a task and press enter. /help for commands, /exit to quit.")
	s.listAgents()
}

func (s *Shell) help() {
	fmt.Print(`  commands:
    /agents          list agents and availability
    /agent <name>    switch the active agent
    /help            show this help
    /exit            leave the session
  routing:
    <task>           send to the active agent
    @<name> <task>   send this one task to a specific agent
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
		if a.Name() == s.current {
			marker = "▸ "
		}
		fmt.Printf("    %s%-10s %s\n", marker, a.Name(), status)
	}
}
