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

	// Label prefixes progress output (used by parallel headless runs so lines
	// from concurrent tasks are attributable). Empty for the interactive path.
	Label string
}

func (o Options) logf(format string, a ...any) {
	if o.Label != "" {
		fmt.Printf("%s ", ui.Dim("["+o.Label+"]"))
	}
	fmt.Printf(format+"\n", a...)
}

// Outcome reports what happened.
type Outcome struct {
	Attempts   int
	ExitCode   int    // last agent exit code (0 = clean; <0 = killed by signal/timeout)
	AgentText  string // combined output the agent produced (for #2 question detection)
	Report     validate.Report
	HadChanges bool
	Accepted   bool
}

// Execute runs the full supervised pipeline once (including retries). The reader
// is shared with the caller so the accept/reject prompt reads the same input.
func Execute(ctx context.Context, in *bufio.Reader, opts Options) (out Outcome, err error) {
	// Record the outcome to memory on a clean (non-error) completion.
	defer func() {
		if err == nil {
			recordMemory(opts, out, outcomeLabel(out))
		}
	}()

	out, err = runLoop(ctx, opts)
	if err != nil {
		return out, err
	}

	// Review the diff.
	diff, err := gitutil.Diff(opts.Dir)
	if err != nil {
		return out, fmt.Errorf("compute diff: %w", err)
	}
	if diff == "" {
		noChangeNotice(opts, out)
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

// taskGuardrail (#1) is prepended to every task so headless agents proceed on
// reasonable assumptions instead of stalling to ask questions they can't be
// answered (there is no interactive channel in headless mode).
const taskGuardrail = "You are running non-interactively and cannot ask follow-up questions. " +
	"Make reasonable assumptions and implement the task; record any important assumptions as brief " +
	"code comments rather than asking for confirmation. Do not wait for input.\n\n"

// noChangeNotice (#2) explains an empty diff. If the agent produced substantial
// output but edited nothing, it likely asked a question or hit an obstacle —
// surface that (and, in a question-shaped case, a hint to clarify) instead of a
// terse "nothing to review" that hides the real situation.
func noChangeNotice(opts Options, out Outcome) {
	text := strings.TrimSpace(out.AgentText)
	if len(text) < 200 {
		opts.logf(ui.Dim("▸ the agent made no changes; nothing to review"))
		return
	}
	if looksLikeQuestion(text) {
		opts.logf(ui.Warn("▸ the agent responded without editing any files — it may need clarification."))
		opts.logf(ui.Dim("  refine your request (add the missing detail) and try again."))
	} else {
		opts.logf(ui.Warn("▸ the agent responded without editing any files (see its output above)."))
	}
}

// looksLikeQuestion heuristically detects a clarifying-question response.
func looksLikeQuestion(text string) bool {
	if strings.Contains(text, "?") {
		return true
	}
	lower := strings.ToLower(text)
	for _, kw := range []string{"could you", "which ", "do you want", "please clarify", "should i", "unclear", "provide more"} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
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

// runLoop performs the dispatch → validate → self-correct loop shared by the
// interactive (Execute) and headless (ExecuteHeadless) paths.
func runLoop(ctx context.Context, opts Options) (out Outcome, err error) {
	prompt := opts.Prompt
	maxAttempts := opts.MaxRetries + 1 // first attempt + retries
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out.Attempts = attempt
		if attempt == 1 {
			opts.logf("%s dispatching to %s", ui.Accent("▸"), ui.Agent(opts.Agent.Name()))
		} else {
			opts.logf("%s %s — %s self-correcting", ui.Accent("▸"),
				ui.Warn(fmt.Sprintf("retry %d/%d", attempt-1, opts.MaxRetries)), ui.Agent(opts.Agent.Name()))
		}

		if opts.Label == "" {
			fmt.Println(ui.Rule(48))
		}
		res, rerr := opts.Agent.Run(ctx, agent.Task{Prompt: taskGuardrail + prompt, Dir: opts.Dir, Timeout: opts.Timeout})
		if opts.Label == "" {
			fmt.Println(ui.Rule(48))
		}
		if rerr != nil {
			return out, fmt.Errorf("agent %q failed to run: %w", opts.Agent.Name(), rerr)
		}
		out.ExitCode = res.ExitCode
		out.AgentText = res.Output
		opts.logf("%s agent exited with code %d in %s", ui.Accent("▸"), res.ExitCode,
			ui.Dim(res.Duration.Round(time.Millisecond).String()))

		out.Report = validate.RunPipeline(ctx, opts.Dir, opts.Stages)
		printReport(opts, out.Report)

		if out.Report.Skipped || out.Report.Passed() {
			break
		}
		if attempt == maxAttempts {
			opts.logf(ui.Warn("▸ retries exhausted"))
			break
		}
		prompt = retryPrompt(opts.Prompt, out.Report)
	}
	return out, nil
}

// ExecuteHeadless runs the loop non-interactively and commits any resulting
// changes to the current branch (used inside a per-task worktree during parallel
// execution). No accept/reject prompt — review happens later at merge time.
func ExecuteHeadless(ctx context.Context, opts Options) (out Outcome, err error) {
	defer func() {
		if err == nil {
			recordMemory(opts, out, headlessOutcome(out))
		}
	}()

	out, err = runLoop(ctx, opts)
	if err != nil {
		return out, err
	}
	diff, derr := gitutil.Diff(opts.Dir)
	if derr != nil {
		return out, fmt.Errorf("compute diff: %w", derr)
	}
	if diff == "" {
		noChangeNotice(opts, out)
		return out, nil
	}
	out.HadChanges = true
	if err := gitutil.Commit(opts.Dir, commitMessage(opts.Prompt)); err != nil {
		return out, fmt.Errorf("commit changes: %w", err)
	}
	opts.logf("%s committed to branch", ui.Success("✓"))
	return out, nil
}

// recordMemory persists an outcome (best-effort) keyed by absolute directory.
func recordMemory(opts Options, out Outcome, outcome string) {
	if opts.Memory == nil {
		return
	}
	dir := opts.Dir
	if abs, e := filepath.Abs(dir); e == nil {
		dir = abs
	}
	if rerr := opts.Memory.Record(memory.Run{
		Dir:      dir,
		Agent:    opts.Agent.Name(),
		Prompt:   opts.Prompt,
		Outcome:  outcome,
		Attempts: out.Attempts,
		Passed:   out.Report.Passed(),
	}, time.Now()); rerr != nil {
		opts.logf("(warning: could not record to memory: %v)", rerr)
	}
}

func headlessOutcome(out Outcome) string {
	if !out.HadChanges {
		return "no-change"
	}
	return "completed"
}

// printReport shows each validation stage's status (label-aware).
func printReport(opts Options, rep validate.Report) {
	if rep.Skipped {
		opts.logf(ui.Dim("▸ validation skipped (no checks configured)"))
		return
	}
	for _, s := range rep.Stages {
		if s.Passed {
			opts.logf("  %s %s", ui.Success("✓"), s.Name)
		} else {
			opts.logf("  %s %s", ui.Danger("✗"), ui.Danger(s.Name+" FAILED"))
			if s.Output != "" && opts.Label == "" {
				fmt.Println(ui.Dim(indent(strings.TrimSpace(s.Output))))
			}
		}
	}
	if rep.Passed() {
		opts.logf(ui.Success("✓ validation passed"))
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
