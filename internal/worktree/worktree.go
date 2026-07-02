// Package worktree isolates parallel agents using git worktrees: each task runs
// in its own checkout on its own branch, so concurrent agents never collide in a
// shared working tree. Accepted branches are merged back into the base with
// conflict detection; rejected ones are discarded.
//
// Worktrees live in a temp directory OUTSIDE the repo, so they never dirty the
// base working tree or trip the supervised clean-tree guard.
package worktree

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Tree is one isolated worktree.
type Tree struct {
	ID     string
	Dir    string
	Branch string
}

// Manager creates and reaps worktrees for a base repository.
type Manager struct {
	repo  string // base repository directory
	root  string // temp dir holding the worktrees
	trees []Tree // everything created, so Cleanup can reap leftovers
}

// NewManager prepares a worktree root for the given repo.
func NewManager(repo string) (*Manager, error) {
	abs, err := filepath.Abs(repo)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "orchestra-wt-")
	if err != nil {
		return nil, err
	}
	return &Manager{repo: abs, root: root}, nil
}

// Add creates a worktree branched from fromRef (e.g. "HEAD"). The branch is
// named orchestra/<id>.
func (m *Manager) Add(id, fromRef string) (Tree, error) {
	branch := "orchestra/" + id
	dir := filepath.Join(m.root, id)
	if _, err := m.git(m.repo, "worktree", "add", "-b", branch, dir, fromRef); err != nil {
		return Tree{}, fmt.Errorf("create worktree %s: %w", id, err)
	}
	t := Tree{ID: id, Dir: dir, Branch: branch}
	m.trees = append(m.trees, t)
	return t, nil
}

// Merge merges the tree's branch into the base repo's current branch. It returns
// conflict=true (and leaves the base tree clean, aborting the merge) when the
// merge does not apply cleanly.
func (m *Manager) Merge(t Tree, message string) (conflict bool, err error) {
	out, err := m.git(m.repo, "merge", "--no-ff", "-m", message, t.Branch)
	if err != nil {
		if strings.Contains(out, "CONFLICT") || strings.Contains(out, "Automatic merge failed") {
			_, _ = m.git(m.repo, "merge", "--abort")
			return true, nil
		}
		return false, fmt.Errorf("merge %s: %s", t.Branch, strings.TrimSpace(out))
	}
	return false, nil
}

// Diff returns the branch's own changes (three-dot: from its merge-base with the
// current HEAD), so it stays correct even after earlier branches in the same
// wave have been merged into the base.
func (m *Manager) Diff(t Tree) (string, error) {
	out, err := m.git(m.repo, "diff", "HEAD..."+t.Branch)
	if err != nil {
		return "", err
	}
	return out, nil
}

// DiffStat returns the change footprint of the tree's branch vs the base HEAD:
// number of files changed and lines added/removed.
func (m *Manager) DiffStat(t Tree) (files, added, removed int, err error) {
	out, gerr := m.git(m.repo, "diff", "--numstat", "HEAD..."+t.Branch)
	if gerr != nil {
		return 0, 0, 0, gerr
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		f := strings.Fields(line) // "<added> <removed> <file>" ("-" for binary)
		if len(f) < 3 {
			continue
		}
		files++
		if n, e := strconv.Atoi(f[0]); e == nil {
			added += n
		}
		if n, e := strconv.Atoi(f[1]); e == nil {
			removed += n
		}
	}
	return files, added, removed, nil
}

// Remove tears down a worktree and deletes its branch.
func (m *Manager) Remove(t Tree) error {
	_, _ = m.git(m.repo, "worktree", "remove", "--force", t.Dir)
	_, _ = m.git(m.repo, "branch", "-D", t.Branch)
	return nil
}

// Cleanup reaps every worktree and branch it created, then removes the root and
// prunes git's records. Safe to call after a partial/interrupted run — any
// worktree not already Removed is torn down here, so nothing leaks.
func (m *Manager) Cleanup() {
	for _, t := range m.trees {
		_, _ = m.git(m.repo, "worktree", "remove", "--force", t.Dir)
		_, _ = m.git(m.repo, "branch", "-D", t.Branch)
	}
	_ = os.RemoveAll(m.root)
	_, _ = m.git(m.repo, "worktree", "prune")
}

// git runs a git command in dir, returning combined output.
func (m *Manager) git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}
