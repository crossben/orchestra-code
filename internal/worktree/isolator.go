package worktree

import "github.com/crossben/orchestra-code/internal/gitutil"

// Isolator gives each concurrent task its own private working directory, shows
// what the task changed, and folds accepted results back into the source
// directory with conflict detection.
//
// Two implementations exist: *Manager (git worktrees, used inside a repository)
// and *CopyIsolator (plain directory copies, used everywhere else). New picks
// the right one.
type Isolator interface {
	// Add creates an isolated tree for task id. fromRef names the git ref to
	// branch from; the copy isolator ignores it and copies the source's
	// current state.
	Add(id, fromRef string) (Tree, error)
	// Diff returns the tree's own changes as a git-style unified diff.
	Diff(t Tree) (string, error)
	// DiffStat summarises Diff: files changed and lines added/removed.
	DiffStat(t Tree) (files, added, removed int, err error)
	// Merge folds the tree's changes into the source. conflict=true means
	// nothing was merged and the source is left as it was.
	Merge(t Tree, message string) (conflict bool, err error)
	// Remove discards one tree.
	Remove(t Tree) error
	// Cleanup discards every tree created, including ones not yet Removed.
	Cleanup()
}

var (
	_ Isolator = (*Manager)(nil)
	_ Isolator = (*CopyIsolator)(nil)
)

// New returns the isolator for dir: git worktrees inside a repository, plain
// directory copies otherwise.
func New(dir string) (Isolator, error) {
	if gitutil.IsRepo(dir) {
		return NewManager(dir)
	}
	return NewCopy(dir)
}
