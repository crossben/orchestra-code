package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// initRepo creates a repo with one committed file and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	git(t, dir, "config", "user.email", "t@t.co")
	git(t, dir, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")
	return dir
}

func writeCommit(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")
}

func TestAddDiffMergeClean(t *testing.T) {
	repo := initRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()

	tree, err := m.Add("a", "HEAD")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	// New, non-conflicting file in the worktree.
	writeCommit(t, tree.Dir, "new.txt", "hello\n")

	diff, err := m.Diff(tree)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !contains(diff, "new.txt") || !contains(diff, "hello") {
		t.Fatalf("diff missing branch changes:\n%s", diff)
	}

	conflict, err := m.Merge(tree, "merge a")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if conflict {
		t.Fatal("unexpected conflict on non-overlapping change")
	}
	if _, err := os.Stat(filepath.Join(repo, "new.txt")); err != nil {
		t.Fatalf("merged file missing in base: %v", err)
	}
}

func TestMergeConflictDetectedAndBaseClean(t *testing.T) {
	repo := initRepo(t)
	m, err := NewManager(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Cleanup()

	// Base changes file.txt on its own branch after the worktree forks.
	tree, err := m.Add("a", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	writeCommit(t, tree.Dir, "file.txt", "worktree-change\n")
	// Meanwhile base changes the same file differently.
	writeCommit(t, repo, "file.txt", "base-change\n")

	conflict, err := m.Merge(tree, "merge a")
	if err != nil {
		t.Fatalf("merge returned error: %v", err)
	}
	if !conflict {
		t.Fatal("expected a conflict, got none")
	}
	// Base tree must be clean (merge aborted), not left mid-conflict.
	out, err := exec.Command("git", "-C", repo, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("base tree not clean after aborted merge:\n%s", out)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
