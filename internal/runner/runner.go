// Package runner executes a child process, streaming its output live and
// capturing its exit code. It is agent-agnostic mechanism: it knows how to run
// a command, not which command each agent needs (that lives in package agent).
package runner

import (
	"bytes"
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
	// Deliberately give the agent NO stdin: headless agents take their prompt
	// from args, and inheriting os.Stdin lets the agent drain input meant for
	// Orchestra's own accept/reject prompt (which silently reverts the work).
	cmd.Stdin = nil
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), spec.Env...)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)

	// Check the context FIRST: a timeout/cancel kills the child via signal, which
	// surfaces as an *exec.ExitError (code -1). Without this ordering that would
	// be misreported as a normal non-zero exit instead of a timeout.
	if ctx.Err() == context.DeadlineExceeded {
		return Result{ExitCode: -1, Duration: dur}, fmt.Errorf("timed out after %s", spec.Timeout)
	}
	if ctx.Err() == context.Canceled {
		return Result{ExitCode: -1, Duration: dur}, context.Canceled
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return Result{ExitCode: exitErr.ExitCode(), Duration: dur}, nil
	}
	if err != nil {
		return Result{ExitCode: -1, Duration: dur}, err
	}
	return Result{ExitCode: 0, Duration: dur}, nil
}

// RunCapture runs the spec and returns the process's stdout as a string, while
// streaming stderr to the parent (so progress/log noise is still visible). Used
// by the planner, which needs to parse an agent's textual answer rather than let
// it flow to the terminal. No stdin is attached.
func RunCapture(ctx context.Context, spec Spec) (string, Result, error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}

	var stdout bytes.Buffer
	cmd := exec.CommandContext(ctx, spec.Bin, spec.Args...)
	cmd.Dir = spec.Dir
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), spec.Env...)

	start := time.Now()
	err := cmd.Run()
	dur := time.Since(start)
	out := stdout.String()

	if ctx.Err() == context.DeadlineExceeded {
		return out, Result{ExitCode: -1, Duration: dur}, fmt.Errorf("timed out after %s", spec.Timeout)
	}
	if ctx.Err() == context.Canceled {
		return out, Result{ExitCode: -1, Duration: dur}, context.Canceled
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		return out, Result{ExitCode: exitErr.ExitCode(), Duration: dur}, nil
	}
	if err != nil {
		return out, Result{ExitCode: -1, Duration: dur}, err
	}
	return out, Result{ExitCode: 0, Duration: dur}, nil
}
