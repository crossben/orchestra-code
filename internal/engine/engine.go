// Package engine is Orchestra's supervised execution pipeline: dispatch a task
// to an agent, validate the result, show the diff, and accept or reject. Both
// the `run` command and the interactive shell drive this same pipeline, so the
// behaviour is identical however you invoke it.
package engine

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/gitutil"
	"github.com/crossben/orchestra/internal/review"
	"github.com/crossben/orchestra/internal/validate"
)

// Options configures a single task execution.
type Options struct {
	Agent       agent.Agent
	Prompt      string
	Dir         string
	TestCommand string
	Timeout     time.Duration

	// CommitOnAccept commits accepted changes so the working tree stays clean
	// between turns. The shell sets this true (each turn builds on the last);
	// the one-shot `run` command leaves changes uncommitted for the user.
	CommitOnAccept bool
}

// Outcome reports what happened, for callers that want to react (the shell
// prints a per-turn summary; `run` maps it to an exit code).
type Outcome struct {
	AgentExit  int
	Duration   time.Duration
	Validation validate.Result
	HadChanges bool
	Accepted   bool
}

// Execute runs the full supervised pipeline once. The reader is shared with the
// caller so the accept/reject prompt reads from the same input stream.
func Execute(ctx context.Context, in *bufio.Reader, opts Options) (Outcome, error) {
	var out Outcome

	// 1. Dispatch the agent.
	fmt.Printf("▸ dispatching to agent %q\n", opts.Agent.Name())
	fmt.Println("──────────────────────────────────────────────")
	res, err := opts.Agent.Run(ctx, agent.Task{
		Prompt:  opts.Prompt,
		Dir:     opts.Dir,
		Timeout: opts.Timeout,
	})
	fmt.Println("──────────────────────────────────────────────")
	if err != nil {
		return out, fmt.Errorf("agent %q failed to run: %w", opts.Agent.Name(), err)
	}
	out.AgentExit = res.ExitCode
	out.Duration = res.Duration
	fmt.Printf("▸ agent exited with code %d in %s\n", res.ExitCode, res.Duration.Round(time.Millisecond))

	// 2. Validate.
	out.Validation = validate.Run(ctx, opts.Dir, opts.TestCommand)
	switch {
	case out.Validation.Skipped:
		fmt.Println("▸ validation skipped (no test command)")
	case out.Validation.Passed:
		fmt.Println("✓ validation passed")
	default:
		fmt.Println("✗ validation FAILED")
		if out.Validation.Output != "" {
			fmt.Println(indent(out.Validation.Output))
		}
	}

	// 3. Review the diff.
	diff, err := gitutil.Diff(opts.Dir)
	if err != nil {
		return out, fmt.Errorf("compute diff: %w", err)
	}
	if diff == "" {
		fmt.Println("▸ no changes to the working tree; nothing to review")
		return out, nil
	}
	out.HadChanges = true

	out.Accepted = review.Prompt(in, diff, out.Validation)
	if out.Accepted {
		if opts.CommitOnAccept {
			if err := gitutil.Commit(opts.Dir, commitMessage(opts.Prompt)); err != nil {
				return out, fmt.Errorf("commit accepted changes: %w", err)
			}
			fmt.Println("✓ changes accepted and committed")
		} else {
			fmt.Println("✓ changes accepted — left in the working tree")
		}
		return out, nil
	}

	if err := gitutil.Restore(opts.Dir); err != nil {
		return out, fmt.Errorf("restore after reject: %w", err)
	}
	fmt.Println("↺ changes rejected — working tree restored")
	return out, nil
}

// commitMessage builds a one-line commit subject from the task prompt.
func commitMessage(prompt string) string {
	line := strings.TrimSpace(prompt)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	const max = 60
	if len(line) > max {
		line = line[:max-1] + "…"
	}
	return "orchestra: " + line
}

func indent(s string) string {
	const pad = "  │ "
	out := pad
	for _, r := range s {
		out += string(r)
		if r == '\n' {
			out += pad
		}
	}
	return out
}
