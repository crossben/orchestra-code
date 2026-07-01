// Package engine is Orchestra's supervised execution pipeline: dispatch a task
// to an agent, validate the result, let the agent self-correct against failing
// checks, then show the diff for a human to accept or reject. Both the `run`
// command and the interactive shell drive this same pipeline.
//
// The self-correction loop (M2) is the point: an agent's output is validated
// (build → lint → test), and if a check fails the failure is fed back to the
// agent for another attempt, up to a bounded number of retries — so the human
// reviews a result that already builds and passes tests whenever possible.
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
	Agent      agent.Agent
	Prompt     string
	Dir        string
	Stages     []validate.Stage // ordered validation pipeline (may be empty)
	MaxRetries int              // self-correction attempts after the first failure
	Timeout    time.Duration

	// CommitOnAccept commits accepted changes so the working tree stays clean
	// between turns. The shell sets this true; one-shot `run` leaves it false.
	CommitOnAccept bool
}

// Outcome reports what happened.
type Outcome struct {
	Attempts   int
	Report     validate.Report
	HadChanges bool
	Accepted   bool
}

// Execute runs the full supervised pipeline once (including retries). The reader
// is shared with the caller so the accept/reject prompt reads the same input.
func Execute(ctx context.Context, in *bufio.Reader, opts Options) (Outcome, error) {
	var out Outcome

	prompt := opts.Prompt
	maxAttempts := opts.MaxRetries + 1 // first attempt + retries
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out.Attempts = attempt
		if attempt == 1 {
			fmt.Printf("▸ dispatching to agent %q\n", opts.Agent.Name())
		} else {
			fmt.Printf("▸ retry %d/%d — agent %q self-correcting\n", attempt-1, opts.MaxRetries, opts.Agent.Name())
		}

		fmt.Println("──────────────────────────────────────────────")
		res, err := opts.Agent.Run(ctx, agent.Task{Prompt: prompt, Dir: opts.Dir, Timeout: opts.Timeout})
		fmt.Println("──────────────────────────────────────────────")
		if err != nil {
			return out, fmt.Errorf("agent %q failed to run: %w", opts.Agent.Name(), err)
		}
		fmt.Printf("▸ agent exited with code %d in %s\n", res.ExitCode, res.Duration.Round(time.Millisecond))

		// Validate.
		out.Report = validate.RunPipeline(ctx, opts.Dir, opts.Stages)
		printReport(out.Report)

		if out.Report.Skipped || out.Report.Passed() {
			break // nothing to fix, or everything passes
		}
		if attempt == maxAttempts {
			fmt.Println("▸ retries exhausted — handing the failing result to you to review")
			break
		}
		// Feed the failure back and loop.
		prompt = retryPrompt(opts.Prompt, out.Report)
	}

	// Review the diff.
	diff, err := gitutil.Diff(opts.Dir)
	if err != nil {
		return out, fmt.Errorf("compute diff: %w", err)
	}
	if diff == "" {
		fmt.Println("▸ no changes to the working tree; nothing to review")
		return out, nil
	}
	out.HadChanges = true

	out.Accepted = review.Prompt(in, diff, out.Report)
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

// retryPrompt asks the agent to fix the failing check, keeping the original goal
// in view. The working tree still holds the agent's prior edits, so it corrects
// in place rather than starting over.
func retryPrompt(original string, rep validate.Report) string {
	return fmt.Sprintf(
		"Your previous changes did not pass validation. %s\n\n"+
			"Fix this in the existing code (your prior edits are still in the working tree). "+
			"The original task was:\n%s",
		rep.FeedbackText(), original,
	)
}

// printReport shows each validation stage's status.
func printReport(rep validate.Report) {
	if rep.Skipped {
		fmt.Println("▸ validation skipped (no checks configured)")
		return
	}
	for _, s := range rep.Stages {
		if s.Passed {
			fmt.Printf("  ✓ %s\n", s.Name)
		} else {
			fmt.Printf("  ✗ %s FAILED\n", s.Name)
			if s.Output != "" {
				fmt.Println(indent(strings.TrimSpace(s.Output)))
			}
		}
	}
	if rep.Passed() {
		fmt.Println("✓ validation passed")
	}
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
