package worktree

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// plainDir builds a non-git source folder:
//
//	keep.txt          "keep\n"          0644
//	run.sh            "#!/bin/sh\n"     0755
//	sub/deep/n.txt    "nested\n"        0600
//	link -> keep.txt  (symlink)
//	node_modules/x.js (excluded by fsdiff rules)
func plainDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "keep.txt", "keep\n", 0o644)
	write(t, dir, "run.sh", "#!/bin/sh\n", 0o755)
	write(t, dir, "sub/deep/n.txt", "nested\n", 0o600)
	write(t, dir, "node_modules/x.js", "heavy\n", 0o644)
	if err := os.Symlink("keep.txt", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func write(t *testing.T, dir, rel, content string, mode os.FileMode) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, mode); err != nil { // defeat umask
		t.Fatal(err)
	}
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func newCopy(t *testing.T, src string) *CopyIsolator {
	t.Helper()
	c, err := NewCopy(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Cleanup)
	return c
}

func TestCopyFidelity(t *testing.T) {
	src := plainDir(t)
	c := newCopy(t, src)
	tree, err := c.Add("a", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Dir == src || !strings.HasPrefix(tree.Dir, c.root) {
		t.Fatalf("copy dir %q not under root %q", tree.Dir, c.root)
	}
	if got := read(t, tree.Dir, "sub/deep/n.txt"); got != "nested\n" {
		t.Fatalf("nested content = %q", got)
	}
	for rel, want := range map[string]os.FileMode{"keep.txt": 0o644, "run.sh": 0o755, "sub/deep/n.txt": 0o600} {
		info, err := os.Stat(filepath.Join(tree.Dir, rel))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %v, want %v", rel, info.Mode().Perm(), want)
		}
	}
	info, err := os.Lstat(filepath.Join(tree.Dir, "link"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("link was not copied as a symlink")
	}
	if target, _ := os.Readlink(filepath.Join(tree.Dir, "link")); target != "keep.txt" {
		t.Fatalf("symlink target = %q", target)
	}
	if _, err := os.Stat(filepath.Join(tree.Dir, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("excluded dir node_modules should not be copied")
	}
}

func TestCopyDiffAddModifyDelete(t *testing.T) {
	src := plainDir(t)
	c := newCopy(t, src)
	tree, err := c.Add("a", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if d, err := c.Diff(tree); err != nil || d != "" {
		t.Fatalf("fresh copy diff = %q, %v; want empty", d, err)
	}
	write(t, tree.Dir, "added.txt", "new\n", 0o644)
	write(t, tree.Dir, "keep.txt", "changed\n", 0o644)
	if err := os.Remove(filepath.Join(tree.Dir, "sub/deep/n.txt")); err != nil {
		t.Fatal(err)
	}
	d, err := c.Diff(tree)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"diff --git a/added.txt b/added.txt\nnew file mode 100644",
		"+new",
		"diff --git a/keep.txt b/keep.txt",
		"-keep\n+changed",
		"diff --git a/sub/deep/n.txt b/sub/deep/n.txt\ndeleted file mode 100644",
		"-nested",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	files, added, removed, err := c.DiffStat(tree)
	if err != nil {
		t.Fatal(err)
	}
	if files != 3 || added != 2 || removed != 2 {
		t.Fatalf("DiffStat = %d files +%d -%d, want 3 +2 -2", files, added, removed)
	}
	// The source is untouched until merge.
	if got := read(t, src, "keep.txt"); got != "keep\n" {
		t.Fatalf("source modified before merge: %q", got)
	}
}

func TestCopyMergeApplies(t *testing.T) {
	src := plainDir(t)
	c := newCopy(t, src)
	tree, _ := c.Add("a", "HEAD")
	write(t, tree.Dir, "pkg/added.txt", "new\n", 0o755)
	write(t, tree.Dir, "keep.txt", "changed\n", 0o644)
	if err := os.Remove(filepath.Join(tree.Dir, "sub/deep/n.txt")); err != nil {
		t.Fatal(err)
	}

	conflict, err := c.Merge(tree, "merge a")
	if err != nil || conflict {
		t.Fatalf("merge = conflict %v, err %v", conflict, err)
	}
	if got := read(t, src, "pkg/added.txt"); got != "new\n" {
		t.Fatalf("added = %q", got)
	}
	if info, _ := os.Stat(filepath.Join(src, "pkg/added.txt")); info.Mode().Perm() != 0o755 {
		t.Errorf("added mode = %v", info.Mode().Perm())
	}
	if got := read(t, src, "keep.txt"); got != "changed\n" {
		t.Fatalf("modified = %q", got)
	}
	if _, err := os.Stat(filepath.Join(src, "sub")); !os.IsNotExist(err) {
		t.Fatal("deleting the only nested file should prune the emptied dirs")
	}
	// Untouched / excluded content survives.
	if got := read(t, src, "node_modules/x.js"); got != "heavy\n" {
		t.Fatalf("excluded file changed: %q", got)
	}
	if target, _ := os.Readlink(filepath.Join(src, "link")); target != "keep.txt" {
		t.Fatal("symlink in source disturbed")
	}
}

func TestCopyMergeConflictWritesNothing(t *testing.T) {
	src := plainDir(t)
	c := newCopy(t, src)
	a, _ := c.Add("a", "HEAD")
	b, _ := c.Add("b", "HEAD")

	write(t, a.Dir, "keep.txt", "from a\n", 0o644)
	write(t, b.Dir, "keep.txt", "from b\n", 0o644)
	write(t, b.Dir, "b-only.txt", "b\n", 0o644)

	if conflict, err := c.Merge(a, "a"); err != nil || conflict {
		t.Fatalf("first merge: conflict %v err %v", conflict, err)
	}
	conflict, err := c.Merge(b, "b")
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("expected a conflict on keep.txt")
	}
	if got := read(t, src, "keep.txt"); got != "from a\n" {
		t.Fatalf("keep.txt = %q, want a's version", got)
	}
	if _, err := os.Stat(filepath.Join(src, "b-only.txt")); !os.IsNotExist(err) {
		t.Fatal("conflicting task must write nothing (all-or-nothing)")
	}
}

func TestCopyMergeConflictOnAddedPath(t *testing.T) {
	src := plainDir(t)
	c := newCopy(t, src)
	a, _ := c.Add("a", "HEAD")
	b, _ := c.Add("b", "HEAD")
	write(t, a.Dir, "same.txt", "a\n", 0o644)
	write(t, b.Dir, "same.txt", "b\n", 0o644)
	if conflict, _ := c.Merge(a, "a"); conflict {
		t.Fatal("unexpected conflict")
	}
	if conflict, _ := c.Merge(b, "b"); !conflict {
		t.Fatal("both adding the same path must conflict")
	}
	if got := read(t, src, "same.txt"); got != "a\n" {
		t.Fatalf("same.txt = %q", got)
	}
}

func TestCopyMergeTraversalGuard(t *testing.T) {
	src := t.TempDir()
	outside := t.TempDir()
	write(t, src, "f.txt", "f\n", 0o644)
	c := newCopy(t, src)
	tree, _ := c.Add("a", "HEAD")

	// The agent creates a real directory "esc" in its copy; meanwhile the
	// source grows a symlink "esc" pointing outside. Writing through it would
	// escape the source tree, so merge must refuse and write nothing.
	write(t, tree.Dir, "esc/pwn.txt", "pwn\n", 0o644)
	write(t, tree.Dir, "ok.txt", "ok\n", 0o644)
	if err := os.Symlink(outside, filepath.Join(src, "esc")); err != nil {
		t.Fatal(err)
	}
	conflict, err := c.Merge(tree, "a")
	if err == nil && !conflict {
		t.Fatal("merge through a symlinked parent must be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "pwn.txt")); !os.IsNotExist(err) {
		t.Fatal("write escaped the source directory")
	}
	if _, err := os.Stat(filepath.Join(src, "ok.txt")); !os.IsNotExist(err) {
		t.Fatal("a refused merge must write nothing")
	}
}

func TestSafeRel(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../x", "a/../../x", "/etc/passwd", "a/./b"} {
		if safeRel(bad) {
			t.Errorf("safeRel(%q) = true", bad)
		}
	}
	for _, ok := range []string{"a", "a/b.txt", ".hidden/x"} {
		if !safeRel(ok) {
			t.Errorf("safeRel(%q) = false", ok)
		}
	}
}

func TestCopyLaterAddSeesMergedState(t *testing.T) {
	src := plainDir(t)
	c := newCopy(t, src)
	a, _ := c.Add("a", "HEAD")
	write(t, a.Dir, "wave1.txt", "one\n", 0o644)
	if conflict, err := c.Merge(a, "a"); err != nil || conflict {
		t.Fatal(conflict, err)
	}
	b, _ := c.Add("b", "HEAD")
	if got := read(t, b.Dir, "wave1.txt"); got != "one\n" {
		t.Fatalf("second-wave copy missing merged file: %q", got)
	}
	if d, _ := c.Diff(b); d != "" {
		t.Fatalf("second-wave diff should start empty, got:\n%s", d)
	}
}

func TestCopyRemoveAndCleanup(t *testing.T) {
	src := plainDir(t)
	c, err := NewCopy(src)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Add("a", "HEAD")
	b, _ := c.Add("b", "HEAD")
	if err := c.Remove(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(a.Dir); !os.IsNotExist(err) {
		t.Fatal("Remove left the copy behind")
	}
	c.Cleanup()
	if _, err := os.Stat(b.Dir); !os.IsNotExist(err) {
		t.Fatal("Cleanup left a copy behind")
	}
	if _, err := os.Stat(c.root); !os.IsNotExist(err) {
		t.Fatal("Cleanup left the root behind")
	}
}

func TestNewPicksBackend(t *testing.T) {
	iso, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer iso.Cleanup()
	if _, ok := iso.(*CopyIsolator); !ok {
		t.Fatalf("plain dir → %T, want *CopyIsolator", iso)
	}
	repo := initRepo(t)
	iso2, err := New(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer iso2.Cleanup()
	if _, ok := iso2.(*Manager); !ok {
		t.Fatalf("repo → %T, want *Manager", iso2)
	}
}
