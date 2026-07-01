// Package runner executes a child process, streaming its output live and
// capturing its exit code. It is agent-agnostic mechanism: it knows how to run
// a command, not which command each agent needs (that lives in package agent).
package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Spec describes one process invocation.
type Spec struct {
	Bin     string        // binary to execute
	Args    []string      // full argument list (agent prefix + task)
	Dir     string        // working directory
	Timeout time.Duration // hard cap on runtime (0 = none)
	Env     []string      // extra environment (appended to os.Environ)
}

// Result reports how the process finished.
type Result struct {
	ExitCode int
	Duration time.Duration
}

// Run executes the spec, streaming stdout/stderr to the parent process. The
// context cancels the child (Ctrl-C). A non-zero exit is returned as a Result,
// not an error — only a failure to start (or a timeout) is an error.
func Run(ctx context.Context, spec Spec) (Result, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), spec.Env...)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	if exitErr, ok := err.(*exec.ExitError); ok {
		return Result{ExitCode: exitErr.ExitCode(), Duration: dur}, nil
	}
	if ctx.Err() == context.DeadlineExceeded {
		return Result{ExitCode: -1, Duration: dur}, fmt.Errorf("timed out after %s", spec.Timeout)
	}
	if err != nil {
		return Result{ExitCode: -1, Duration: dur}, err
	}
	return Result{ExitCode: 0, Duration: dur}, nil
}
