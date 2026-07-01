// Package gitutil wraps the git commands the supervised loop depends on:
// detecting a repo, checking the tree is clean before a run, showing what the
// agent changed, and restoring the tree when the human rejects.
package gitutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// IsRepo reports whether dir is inside a git working tree.
func IsRepo(dir string) bool {
	out, err := run(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// IsClean reports whether the working tree has no uncommitted changes
// (tracked or untracked). The supervised loop reverts on reject, so we refuse
// to run on a dirty tree unless the caller forces it.
func IsClean(dir string) (bool, error) {
	out, err := run(dir, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

// Diff returns a unified diff of everything the agent changed, including new
// files. It marks untracked files with intent-to-add so their contents appear
// in the diff, then unstages those markers so an accepted change stays clean.
func Diff(dir string) (string, error) {
	// -N (intent-to-add) makes `git diff` include untracked file contents.
	if _, err := run(dir, "add", "-A", "-N"); err != nil {
		return "", err
	}
	diff, err := run(dir, "diff")
	// Unstage the intent-to-add markers regardless, so we don't leave the index
	// in a half-staged state on accept.
	_, _ = run(dir, "reset", "-q")
	if err != nil {
		return "", err
	}
	return diff, nil
}

// Restore reverts the working tree to its pre-run state: undo tracked
// modifications and delete any files/directories the agent created.
//
// Safe because the caller guarantees (via IsClean) the tree was clean before
// the run, unless --force was used.
func Restore(dir string) error {
	// Revert modifications to tracked files. Ignore the error when there is no
	// commit yet (a fresh repo has nothing to check out); git clean handles the
	// new-files case below.
	_, _ = run(dir, "checkout", "--", ".")
	// Remove newly created files and directories.
	if _, err := run(dir, "clean", "-fd"); err != nil {
		return err
	}
	return nil
}

// run executes a git command in dir and returns its stdout, wrapping failures
// with stderr for context.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.String(), nil
}
