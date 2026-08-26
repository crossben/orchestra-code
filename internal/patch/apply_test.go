package patch

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a clean git repo in a temp dir with one commit, mirroring
// internal/worktree's test setup. Skips the test when git is unavailable.
func initRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-qm", "init")
	return dir
}

// mustClean fails when dir has uncommitted changes.
func mustClean(t *testing.T, dir string) {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v: %s", err, out)
	}
	if strings.TrimSpace(string(out)) != "" {
		t.Fatalf("expected clean tree, got:\n%s", out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const modifyMainDiff = `diff --git a/main.go b/main.go
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+// touched

 func main() {}
`

func TestApplyModifiesExistingFile(t *testing.T) {
	dir := initRepo(t)
	p := Patch{Diffs: []DiffText{{Raw: modifyMainDiff}}}
	if err := Apply(context.Background(), dir, p); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(dir, "main.go"))
	if !strings.Contains(got, "// touched") {
		t.Fatalf("change missing:\n%s", got)
	}
}

func TestApplyCreatesNewFile(t *testing.T) {
	dir := initRepo(t)
	const diff = `diff --git a/new/pkg/util.go b/new/pkg/util.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/new/pkg/util.go
@@ -0,0 +1,3 @@
+package util
+
+func Hi() string { return "hi" }
`
	if err := Apply(context.Background(), dir, Patch{Diffs: []DiffText{{Raw: diff}}}); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, filepath.Join(dir, "new", "pkg", "util.go"))
	if !strings.Contains(got, "package util") {
		t.Fatalf("unexpected content:\n%s", got)
	}
}

func TestApplyDeletesFile(t *testing.T) {
	dir := initRepo(t)
	const diff = `diff --git a/README.md b/README.md
deleted file mode 100644
index 1111111..0000000
--- a/README.md
+++ /dev/null
@@ -1 +0,0 @@
-# repo
`
	if err := Apply(context.Background(), dir, Patch{Diffs: []DiffText{{Raw: diff}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !os.IsNotExist(err) {
		t.Fatal("README.md should be gone")
	}
}

func TestApplyConflictingDiffLeavesTreeUntouched(t *testing.T) {
	dir := initRepo(t)
	// A diff against content that doesn't exist.
	const bad = `diff --git a/missing.txt b/missing.txt
--- a/missing.txt
+++ b/missing.txt
@@ -1 +0,0 @@
-old line
`
	err := Apply(context.Background(), dir, Patch{Diffs: []DiffText{{Raw: bad}}})
	if err == nil {
		t.Fatal("expected conflict error")
	}
	if !strings.Contains(err.Error(), "nothing was written") {
		t.Fatalf("error should promise atomicity: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(dir, "missing.txt")); !os.IsNotExist(serr) {
		t.Fatal("conflicting patch created a file")
	}
	mustClean(t, dir)
}

func TestApplyFileWritesInPlainDir(t *testing.T) {
	dir := t.TempDir() // deliberately NOT a git repo
	p := Patch{Files: []FileWrite{
		{Path: "nested/dir/app.txt", Content: []byte("hello")},
	}}
	if err := Apply(context.Background(), dir, p); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "nested", "dir", "app.txt")); got != "hello" {
		t.Fatalf("content: %q", got)
	}
}

func TestApplyUpdateAndDeleteViaFileWrites(t *testing.T) {
	dir := initRepo(t)
	p := Patch{Files: []FileWrite{
		{Path: "README.md", Content: []byte("# rewritten\n")},
		{Path: "main.go", Delete: true},
	}}
	if err := Apply(context.Background(), dir, p); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "README.md")); got != "# rewritten\n" {
		t.Fatalf("readme: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "main.go")); !os.IsNotExist(err) {
		t.Fatal("main.go should be deleted")
	}
	// No stray temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".orchestra-patch-") {
			t.Fatalf("temp file leaked: %s", e.Name())
		}
	}
}

func TestApplyRejectsUnsafeFilePathsBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	p := Patch{Files: []FileWrite{
		{Path: "ok.txt", Content: []byte("fine")},
		{Path: "../escape.txt", Content: []byte("bad")},
	}}
	if err := Apply(context.Background(), dir, p); err == nil {
		t.Fatal("expected error for traversal path")
	}
	if _, err := os.Stat(filepath.Join(dir, "ok.txt")); !os.IsNotExist(err) {
		t.Fatal("no file should be written when any path is invalid")
	}
	if _, err := os.Stat(filepath.Join(t.TempDir(), "escape.txt")); err == nil {
		t.Fatal("impossible")
	}
}

func TestApplyEmptyPatchIsNoop(t *testing.T) {
	dir := initRepo(t)
	if err := Apply(context.Background(), dir, Patch{}); err != nil {
		t.Fatal(err)
	}
	mustClean(t, dir)
}
