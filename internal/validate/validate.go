// Package validate runs the project's test command against the agent's output.
//
// M0 keeps this minimal: one configurable command, pass/fail. M2 grows it into
// a build → lint → test pipeline that can feed failures back to the agent.
package validate

import (
	"context"
	"os/exec"
	"strings"
)

// Result reports the outcome of validation.
type Result struct {
	Skipped bool   // no test command was configured
	Passed  bool   // the command exited 0
	Output  string // combined stdout+stderr (shown to the human on failure)
}

// Run executes testCmd via the shell in dir. An empty testCmd is a skip, not a
// pass — the caller distinguishes "verified" from "unverified" in the review.
func Run(ctx context.Context, dir, testCmd string) Result {
	if strings.TrimSpace(testCmd) == "" {
		return Result{Skipped: true}
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", testCmd)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()

	return Result{
		Passed: err == nil,
		Output: string(out),
	}
}
