// Package runner executes a coding-agent CLI as a child process, streaming its
// output live and capturing its exit code. This is the engine of Orchestra:
// the interactive shell and the `run` command are both thin layers over it.
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Config describes a single agent invocation.
type Config struct {
	Agent   string        // agent name (claude|codex|gemini) or an explicit binary
	Task    string        // the task prompt handed to the agent
	Dir     string        // working directory to run in
	Timeout time.Duration // hard cap on the agent's runtime (0 = no timeout)
	Env     []string      // extra environment (appended to os.Environ)
}

// Result reports how the agent process finished.
type Result struct {
	ExitCode int
	Duration time.Duration
}

// knownAgents maps an agent name to the argument prefix that puts its CLI into
// non-interactive ("headless") mode, before the task prompt is appended.
//
// M0 supports one working agent end to end; the others are wired the same way
// so a second agent in M1 is a one-line addition, not a refactor.
var knownAgents = map[string][]string{
	"claude": {"-p"},     // Claude Code headless:  claude -p "<task>"
	"codex":  {"exec"},   // Codex CLI headless:    codex exec "<task>"
	"gemini": {"-p"},     // Gemini CLI headless:   gemini -p "<task>"
}

// Run dispatches the agent, streaming stdout/stderr to the parent process and
// returning the exit code. The provided context cancels the child (Ctrl-C).
func Run(ctx context.Context, cfg Config) (Result, error) {
	bin, prefix := resolve(cfg.Agent)

	if cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Timeout)
		defer cancel()
	}

	args := append(append([]string{}, prefix...), cfg.Task)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = cfg.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), cfg.Env...)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	// A non-zero exit is a normal outcome (the agent ran but reported failure),
	// not a runner error — report the code and let the caller decide.
	if exitErr, ok := err.(*exec.ExitError); ok {
		return Result{ExitCode: exitErr.ExitCode(), Duration: dur}, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Result{ExitCode: -1, Duration: dur}, fmt.Errorf("agent timed out after %s", cfg.Timeout)
	}
	if err != nil {
		// Couldn't start the process at all (e.g. binary not found).
		return Result{ExitCode: -1, Duration: dur}, err
	}
	return Result{ExitCode: 0, Duration: dur}, nil
}

// resolve maps an agent name to its binary and headless-mode arg prefix.
// Unknown names are treated as an explicit binary with no prefix, so
// `--agent ./my-agent` also works.
func resolve(agent string) (bin string, prefix []string) {
	if p, ok := knownAgents[agent]; ok {
		return agent, p
	}
	return agent, nil
}
