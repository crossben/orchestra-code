package fsdiff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
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

func TestCaptureExcludesNoise(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.txt"), "yes")
	writeFile(t, filepath.Join(dir, "node_modules", "pkg", "x.js"), "dep")
	writeFile(t, filepath.Join(dir, ".git", "HEAD"), "ref")
	writeFile(t, filepath.Join(dir, "logo.png"), "fake png")
	writeFile(t, filepath.Join(dir, "notes.pyc"), "bytecode")

	snap, err := Capture(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snap.Files["keep.txt"]; !ok {
		t.Error("keep.txt should be captured")
	}
	for _, gone := range []string{"node_modules/pkg/x.js", ".git/HEAD", "logo.png", "notes.pyc"} {
		if _, ok := snap.Files[gone]; ok {
			t.Errorf("%s should be excluded", gone)
		}
	}
}

func TestCaptureWithholdsOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("a", MaxFileBytes+1)
	writeFile(t, filepath.Join(dir, "big.log"), big)

	snap, err := Capture(dir)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := snap.Files["big.log"]
	if !ok || !f.Withheld {
		t.Fatalf("expected withheld entry, got %+v", f)
	}
}

const baseReadme = "# title\n\nintro line\n"

func TestDiffAddedModifiedDeleted(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "readme.md"), baseReadme)
	writeFile(t, filepath.Join(dir, "gone.txt"), "delete me\n")

	before, err := Capture(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Mutate: modify readme (middle change), delete gone.txt, add new.txt.
	writeFile(t, filepath.Join(dir, "readme.md"), "# title\n\nchanged intro line\n")
	if err := os.Remove(filepath.Join(dir, "gone.txt")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "new", "thing.txt"), "brand new\n")

	diff, err := Diff(dir, before)
	if err != nil {
		t.Fatal(err)
	}

	checks := []string{
		"diff --git a/readme.md b/readme.md",
		"--- a/readme.md",
		"+++ b/readme.md",
		"-intro line",
		"+changed intro line",
		"@@ -1,3 +1,3 @@",

		"diff --git a/gone.txt b/gone.txt",
		"deleted file mode 100644",
		"+++ /dev/null",
		"-delete me",

		"diff --git a/new/thing.txt b/new/thing.txt",
		"new file mode 100644",
		"--- /dev/null",
		"+brand new",
	}
	for _, want := range checks {
		if !strings.Contains(diff, want) {
			t.Errorf("diff missing %q:\n%s", want, diff)
		}
	}
	if strings.Count(diff, "diff --git") != 3 {
		t.Errorf("unexpected extra sections:\n%s", diff)
	}
}

func TestDiffIdenticalIsEmpty(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "same\n")
	before, _ := Capture(dir)
	diff, err := Diff(dir, before)
	if err != nil {
		t.Fatal(err)
	}
	if diff != "" {
		t.Fatalf("expected empty diff, got:\n%s", diff)
	}
}

func TestDiffNoTrailingNewlineMarker(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f.txt"), "one\ntwo")
	before, _ := Capture(dir)
	writeFile(t, filepath.Join(dir, "f.txt"), "one\nTWO")
	diff, err := Diff(dir, before)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-two\n\\ No newline at end of file") {
		t.Errorf("expected no-newline marker after -two:\n%s", diff)
	}
}

func TestDiffBinaryChangeIsOpaque(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "blob.dat"), "\x00\x01binary v1")
	before, _ := Capture(dir)
	writeFile(t, filepath.Join(dir, "blob.dat"), "\x00\x01binary v2")
	diff, err := Diff(dir, before)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "Binary files a/blob.dat and b/blob.dat differ") {
		t.Errorf("expected opaque binary section:\n%s", diff)
	}
}

func TestDiffEmptyToContentAndBack(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "empty.txt"), "")
	before, _ := Capture(dir)
	writeFile(t, filepath.Join(dir, "empty.txt"), "now has lines\n")
	diff, err := Diff(dir, before)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "@@ -0,0 +1 @@") || !strings.Contains(diff, "+now has lines") {
		t.Errorf("empty→content header wrong:\n%s", diff)
	}
}

func TestRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "src", "app.go"), "package main\n")
	writeFile(t, filepath.Join(dir, "keep.txt"), "original")
	writeFile(t, filepath.Join(dir, "doomed.txt"), "bye")
	mode0755 := os.FileMode(0o755)
	if err := os.WriteFile(filepath.Join(dir, "script.sh"), []byte("#!/bin/sh\n"), mode0755); err != nil {
		t.Fatal(err)
	}

	before, err := Capture(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Wreck the place: modify two, delete two, create two.
	writeFile(t, filepath.Join(dir, "keep.txt"), "modified!")
	writeFile(t, filepath.Join(dir, "src", "app.go"), "package main // hacked\n")
	os.Remove(filepath.Join(dir, "doomed.txt"))
	os.Remove(filepath.Join(dir, "script.sh"))
	writeFile(t, filepath.Join(dir, "created.txt"), "new junk")
	writeFile(t, filepath.Join(dir, "tmp", "junk.log"), "junk")

	if err := Restore(dir, before); err != nil {
		t.Fatal(err)
	}

	if got := readFile(t, filepath.Join(dir, "keep.txt")); got != "original" {
		t.Errorf("keep.txt not restored: %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "src", "app.go")); got != "package main\n" {
		t.Errorf("src/app.go not restored: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "doomed.txt")); err != nil {
		t.Error("doomed.txt should be recreated")
	}
	info, err := os.Stat(filepath.Join(dir, "script.sh"))
	if err != nil {
		t.Fatal("script.sh should be restored")
	}
	if info.Mode().Perm() != mode0755 {
		t.Errorf("mode not restored: %v", info.Mode())
	}
	for _, gone := range []string{"created.txt", "tmp/junk.log"} {
		if _, err := os.Stat(filepath.Join(dir, gone)); !os.IsNotExist(err) {
			t.Errorf("%s should have been removed", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "tmp")); !os.IsNotExist(err) {
		t.Error("emptied tmp/ dir should be pruned")
	}
}

func TestLineEditsMinimalScript(t *testing.T) {
	a := []string{"a", "b", "c", "d"}
	b := []string{"a", "x", "c", "d"}
	edits := lineEdits(a, b)
	var gotDel, gotIns, gotEq int
	for _, e := range edits {
		switch e.kind {
		case opDel:
			gotDel++
			if e.line != "b" {
				t.Errorf("wrong deletion: %q", e.line)
			}
		case opIns:
			gotIns++
			if e.line != "x" {
				t.Errorf("wrong insertion: %q", e.line)
			}
		default:
			gotEq++
		}
	}
	if gotDel != 1 || gotIns != 1 || gotEq != 3 {
		t.Errorf("not minimal: del=%d ins=%d eq=%d", gotDel, gotIns, gotEq)
	}
}

func TestLineEditsReconstructB(t *testing.T) {
	cases := [][2][]string{
		{{"a", "b", "c"}, {"a", "x", "c"}},
		{{"1", "2"}, {"2", "1"}},
		{{"only"}, {}},
		{{}, {"fresh"}},
		{{"k", "e", "e", "p"}, {"k", "e", "e", "p"}},
	}
	for i, tc := range cases {
		a, b := tc[0], tc[1]
		var out []string
		var dels int
		for _, e := range lineEdits(a, b) {
			switch e.kind {
			case opIns, opEq:
				out = append(out, e.line)
			case opDel:
				dels++
			}
		}
		if strings.Join(out, "|") != strings.Join(b, "|") {
			t.Errorf("case %d: reconstruction = %v, want %v", i, out, b)
		}
		if dels > len(a) {
			t.Errorf("case %d: too many deletions (%d)", i, dels)
		}
	}
}
