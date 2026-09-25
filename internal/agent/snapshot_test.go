package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// snapRepo builds a git repo fixture for snapshot tests. Skips when git is
// unavailable (consistent with worktree/patch test suites).
func snapRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	git("init", "-q")
	write(t, filepath.Join(dir, "main.go"), "package main\n\nfunc main() {}\n")
	write(t, filepath.Join(dir, "docs", "notes.md"), "# notes\nsome prose\n")
	write(t, filepath.Join(dir, "logo.png"), []byte{0x89, 'P', 'N', 'G', 0x00, 0x01})
	write(t, filepath.Join(dir, "go.sum"), strings.Repeat("module v1.0.0 h1:abc=\n", 50))
	git("add", "-A")
	return dir
}

func write(t *testing.T, path string, content any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var data []byte
	switch c := content.(type) {
	case string:
		data = []byte(c)
	case []byte:
		data = c
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotListsAndInlinesTextFiles(t *testing.T) {
	dir := snapRepo(t)
	snap, err := buildSnapshot(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"main.go", "docs/notes.md", "package main", "# notes"} {
		if !strings.Contains(snap, want) {
			t.Errorf("snapshot missing %q:\n%s", want, snap)
		}
	}
}

func TestSnapshotSkipsBinaryAndLockfiles(t *testing.T) {
	dir := snapRepo(t)
	snap, err := buildSnapshot(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap, "PNG") && strings.Contains(strings.SplitN(snap, "=== CONTENTS ===", 2)[1], "----- logo.png") {
		t.Error("binary file content should not be inlined")
	}
	if contents := strings.SplitN(snap, "=== CONTENTS ===", 2)[1]; strings.Contains(contents, "go.sum") {
		t.Error("lockfile should not be inlined")
	}
	for _, tag := range []string{"skipped (binary)", "skipped (lockfile)"} {
		if !strings.Contains(snap, tag) {
			t.Errorf("expected note %q in:\n%s", tag, snap)
		}
	}
}

func TestSnapshotRespectsBudgetWithTruncationMarker(t *testing.T) {
	dir := snapRepo(t)
	write(t, filepath.Join(dir, "big.txt"), strings.Repeat("a", 4096))
	snap, err := buildSnapshot(dir, 256)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap, "[truncated at") {
		t.Errorf("expected truncation marker:\n%s", snap)
	}
	if !strings.Contains(snap, "not shown (budget exhausted)") {
		t.Errorf("expected exhausted-budget notes:\n%s", snap)
	}
	// The truncated body must not exceed the budget by much (header + list overhead allowed).
	if len(snap) > 256+4096 { // generous upper bound; the point is big.txt isn't fully inlined twice
		t.Fatalf("snapshot suspiciously large: %d bytes", len(snap))
	}
}

func TestSnapshotIgnoresGitignoredFiles(t *testing.T) {
	dir := snapRepo(t)
	write(t, filepath.Join(dir, ".gitignore"), "secret.txt\n")
	write(t, filepath.Join(dir, "secret.txt"), "hunter2\n")
	snap, err := buildSnapshot(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(snap, "hunter2") {
		t.Error("gitignored file leaked into the snapshot")
	}
}

// TestBuildSnapshotWithoutGit covers plain directories: no .git anywhere, so
// the git ls-files path fails and the filesystem-walk fallback must kick in.
func TestBuildSnapshotWithoutGit(t *testing.T) {
	dir := t.TempDir() // deliberately not a git repository
	write(t, filepath.Join(dir, "src", "app.py"), "print('hello')\n")
	write(t, filepath.Join(dir, "node_modules", "dep.js"), "should be skipped")
	write(t, filepath.Join(dir, "logo.png"), "\x00\x01binary")

	snap, err := buildSnapshot(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snap, "src/app.py") || !strings.Contains(snap, "print('hello')") {
		t.Errorf("fallback walk missed source file:\n%s", snap)
	}
	if strings.Contains(strings.SplitN(snap, "=== CONTENTS ===", 2)[1], "dep.js") {
		t.Error("dependency dir leaked into contents")
	}
}
