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
	"path/filepath"
	"strings"
	"time"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/gitutil"
	"github.com/crossben/orchestra/internal/memory"
	"github.com/crossben/orchestra/internal/review"
	"github.com/crossben/orchestra/internal/ui"
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

	// Memory, if non-nil, records the outcome of this execution for history and
	// the preferred-agent hint.
	Memory *memory.Store
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
func Execute(ctx context.Context, in *bufio.Reader, opts Options) (out Outcome, err error) {
	// Record the outcome to memory on a clean (non-error) completion.
	defer func() {
		if err != nil || opts.Memory == nil {
			return
		}
		dir := opts.Dir
		if abs, e := filepath.Abs(dir); e == nil {
			dir = abs // match `orchestra history`, which keys by absolute dir
		}
		if rerr := opts.Memory.Record(memory.Run{
			Dir:      dir,
			Agent:    opts.Agent.Name(),
			Prompt:   opts.Prompt,
			Outcome:  outcomeLabel(out),
			Attempts: out.Attempts,
			Passed:   out.Report.Passed(),
		}, time.Now()); rerr != nil {
			fmt.Printf("  (warning: could not record to memory: %v)\n", rerr)
		}
	}()

	prompt := opts.Prompt
	maxAttempts := opts.MaxRetries + 1 // first attempt + retries
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out.Attempts = attempt
		if attempt == 1 {
			fmt.Printf("%s dispatching to %s\n", ui.Accent("▸"), ui.Agent(opts.Agent.Name()))
		} else {
			fmt.Printf("%s %s — %s self-correcting\n", ui.Accent("▸"),
				ui.Warn(fmt.Sprintf("retry %d/%d", attempt-1, opts.MaxRetries)), ui.Agent(opts.Agent.Name()))
		}

		fmt.Println(ui.Rule(48))
		res, err := opts.Agent.Run(ctx, agent.Task{Prompt: prompt, Dir: opts.Dir, Timeout: opts.Timeout})
		fmt.Println(ui.Rule(48))
		if err != nil {
			return out, fmt.Errorf("agent %q failed to run: %w", opts.Agent.Name(), err)
		}
		fmt.Printf("%s agent exited with code %d in %s\n", ui.Accent("▸"), res.ExitCode,
			ui.Dim(res.Duration.Round(time.Millisecond).String()))

		// Validate.
		out.Report = validate.RunPipeline(ctx, opts.Dir, opts.Stages)
		printReport(out.Report)

		if out.Report.Skipped || out.Report.Passed() {
			break // nothing to fix, or everything passes
		}
		if attempt == maxAttempts {
			fmt.Println(ui.Warn("▸ retries exhausted — handing the failing result to you to review"))
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
		fmt.Println(ui.Dim("▸ no changes to the working tree; nothing to review"))
		return out, nil
	}
	out.HadChanges = true

	out.Accepted = review.Prompt(in, diff, out.Report)
	if out.Accepted {
		if opts.CommitOnAccept {
			if err := gitutil.Commit(opts.Dir, commitMessage(opts.Prompt)); err != nil {
				return out, fmt.Errorf("commit accepted changes: %w", err)
			}
			fmt.Println(ui.Success("✓ changes accepted and committed"))
		} else {
			fmt.Println(ui.Success("✓ changes accepted — left in the working tree"))
		}
		return out, nil
	}

	if err := gitutil.Restore(opts.Dir); err != nil {
		return out, fmt.Errorf("restore after reject: %w", err)
	}
	fmt.Println(ui.Warn("↺ changes rejected — working tree restored"))
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
		fmt.Println(ui.Dim("▸ validation skipped (no checks configured)"))
		return
	}
	for _, s := range rep.Stages {
		if s.Passed {
			fmt.Printf("  %s %s\n", ui.Success("✓"), s.Name)
		} else {
			fmt.Printf("  %s %s\n", ui.Danger("✗"), ui.Danger(s.Name+" FAILED"))
			if s.Output != "" {
				fmt.Println(ui.Dim(indent(strings.TrimSpace(s.Output))))
			}
		}
	}
	if rep.Passed() {
		fmt.Println(ui.Success("✓ validation passed"))
	}
}

// outcomeLabel maps an Outcome to a memory outcome string.
func outcomeLabel(out Outcome) string {
	switch {
	case !out.HadChanges:
		return "no-change"
	case out.Accepted:
		return "accepted"
	default:
		return "rejected"
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
