// Package validate runs an ordered pipeline of shell commands (build → lint →
// test) against the agent's output. This is Orchestra's trust layer: an agent's
// diff has to prove itself before a human is asked to look at it, and a failing
// stage is fed back to the agent so it can self-correct (see package engine).
//
// The pipeline stops at the first failing stage — there's no point running tests
// if the build is broken, and the earliest failure is the most useful feedback.
package validate

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Stage is one named validation command, e.g. {"build", "go build ./..."}.
type Stage struct {
	Name    string
	Command string
}

// StageResult is the outcome of running one stage.
type StageResult struct {
	Name   string
	Passed bool
	Output string // combined stdout+stderr (used for retry feedback and display)
}

// Report is the outcome of a pipeline run.
type Report struct {
	Stages  []StageResult // stages actually run, in order, ending at any failure
	Skipped bool          // true when no stages were configured

	commands map[string]string // stage name → command, for FeedbackText
}

// Passed reports whether every run stage passed. A skipped (empty) pipeline is
// not "passed" — it's unverified; callers distinguish via Skipped.
func (r Report) Passed() bool {
	if r.Skipped || len(r.Stages) == 0 {
		return false
	}
	for _, s := range r.Stages {
		if !s.Passed {
			return false
		}
	}
	return true
}

// Failure returns the first failed stage, if any.
func (r Report) Failure() (StageResult, bool) {
	for _, s := range r.Stages {
		if !s.Passed {
			return s, true
		}
	}
	return StageResult{}, false
}

// FeedbackText renders a failing stage as text to hand back to the agent.
func (r Report) FeedbackText() string {
	f, ok := r.Failure()
	if !ok {
		return ""
	}
	return fmt.Sprintf("The %q check failed:\n\n$ %s\n%s", f.Name, stageCommand(r, f.Name), strings.TrimSpace(f.Output))
}

// RunPipeline runs stages in order in dir, stopping at the first failure.
func RunPipeline(ctx context.Context, dir string, stages []Stage) Report {
	if len(stages) == 0 {
		return Report{Skipped: true}
	}
	var rep Report
	for _, st := range stages {
		if strings.TrimSpace(st.Command) == "" {
			continue
		}
		cmd := exec.CommandContext(ctx, "sh", "-c", st.Command)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		res := StageResult{Name: st.Name, Passed: err == nil, Output: string(out)}
		rep.Stages = append(rep.Stages, res)
		if !res.Passed {
			break // stop-on-first-failure
		}
	}
	if len(rep.Stages) == 0 {
		rep.Skipped = true
	}
	// Remember commands so FeedbackText can echo them.
	rep.commands = map[string]string{}
	for _, st := range stages {
		rep.commands[st.Name] = st.Command
	}
	return rep
}

// commands is populated by RunPipeline for FeedbackText; unexported so it stays
// an implementation detail of the report.
func stageCommand(r Report, name string) string {
	if r.commands == nil {
		return ""
	}
	return r.commands[name]
}
