package patch

import (
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/main.go b/main.go
index 1111111..2222222 100644
--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+
 func main() {}
`

func TestExtractFencedDiff(t *testing.T) {
	text := "Here is my change:\n\n```diff\n" + sampleDiff + "```\n\nHope that helps."
	p, ok := Extract(text)
	if !ok {
		t.Fatal("expected a patch")
	}
	if len(p.Diffs) != 1 || p.Diffs[0].Raw != strings.TrimSuffix(sampleDiff, "\n") {
		t.Fatalf("unexpected diffs: %+v", p.Diffs)
	}
	if len(p.Files) != 0 {
		t.Fatalf("unexpected files: %+v", p.Files)
	}
}

func TestExtractUnlabeledFenceWithDiff(t *testing.T) {
	text := "```\n" + sampleDiff + "```"
	p, ok := Extract(text)
	if !ok || len(p.Diffs) != 1 {
		t.Fatalf("got ok=%v diffs=%d", ok, len(p.Diffs))
	}
}

func TestExtractMultipleFences(t *testing.T) {
	text := "```go\nmain.go\npackage main\n```\ntext between\n```diff\ndiff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n```"
	p, ok := Extract(text)
	if !ok {
		t.Fatal("expected patch")
	}
	if len(p.Files) != 1 || p.Files[0].Path != "main.go" {
		t.Fatalf("files: %+v", p.Files)
	}
	if string(p.Files[0].Content) != "package main" {
		t.Fatalf("content: %q", p.Files[0].Content)
	}
	if len(p.Diffs) != 1 || !strings.HasPrefix(p.Diffs[0].Raw, "diff --git") {
		t.Fatalf("diffs: %+v", p.Diffs)
	}
}

func TestExtractFullFileBlocksWithBlankSeparator(t *testing.T) {
	text := "```\ninternal/app/app.go\n\npackage app\n\nfunc A() {}\n```"
	p, ok := Extract(text)
	if !ok {
		t.Fatal("expected patch")
	}
	f := p.Files[0]
	if f.Path != "internal/app/app.go" {
		t.Fatalf("path: %q", f.Path)
	}
	if string(f.Content) != "package app\n\nfunc A() {}" {
		t.Fatalf("content: %q", f.Content)
	}
}

func TestExtractDeleteMarker(t *testing.T) {
	text := "```\nold/legacy.txt\n<DELETE>\n```"
	p, ok := Extract(text)
	if !ok {
		t.Fatal("expected patch")
	}
	f := p.Files[0]
	if !f.Delete || f.Path != "old/legacy.txt" {
		t.Fatalf("file: %+v", f)
	}
	if p.Empty() {
		t.Fatal("a deletion is content; patch must not report Empty")
	}
}

func TestExtractProseOnlyReturnsFalse(t *testing.T) {
	text := "I could not figure out which file you mean. Could you clarify?"
	if _, ok := Extract(text); ok {
		t.Fatal("expected no patch from prose")
	}
}

func TestExtractEmpty(t *testing.T) {
	for _, s := range []string{"", "```\n```", "just words"} {
		if _, ok := Extract(s); ok {
			t.Fatalf("expected no patch for %q", s)
		}
	}
}

func TestExtractRejectsUnsafePaths(t *testing.T) {
	for _, bad := range []string{"../evil.txt", "/abs/path.txt", "a b.txt", "", ".."} {
		text := "```\n" + bad + "\ncontent\n```"
		if p, ok := Extract(text); ok {
			t.Fatalf("accepted unsafe path %q: %+v", bad, p.Files)
		}
	}
}

func TestExtractNormalizesDotSlash(t *testing.T) {
	text := "```\n./cmd/a/main.go\npackage main\n```"
	p, _ := Extract(text)
	if len(p.Files) != 1 || p.Files[0].Path != "cmd/a/main.go" {
		t.Fatalf("files: %+v", p.Files)
	}
}

func TestExtractUnterminatedFenceFallsBackToRawDiff(t *testing.T) {
	// A truncated reply inside an unterminated fence still exposes its
	// diff-shaped content through the unfenced fallback; if it's corrupt,
	// Apply rejects it safely via git apply --check.
	text := "```diff\ndiff --git a/x b/x"
	p, ok := Extract(text)
	if !ok || len(p.Diffs) != 1 || !strings.HasPrefix(p.Diffs[0].Raw, "diff --git") {
		t.Fatalf("expected fallback capture, got ok=%v %+v", ok, p)
	}
	if len(p.Files) != 0 {
		t.Fatalf("unexpected files: %+v", p.Files)
	}
}

func TestCleanRelPath(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"a/b.go", "a/b.go", true},
		{"./a/b.go", "a/b.go", true},
		{"a/../b.go", "", false}, // contains traversal after clean
		{"../x", "", false},
		{"/etc/passwd", "", false},
		{"", "", false},
		{"with space.txt", "", false},
		{"back\\slash.txt", "", false},
	}
	for _, tc := range cases {
		got, ok := cleanRelPath(tc.in)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("cleanRelPath(%q) = %q,%v want %q,%v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestPatchEmpty(t *testing.T) {
	var p Patch
	if !p.Empty() {
		t.Fatal("zero patch should be empty")
	}
	p.Files = append(p.Files, FileWrite{Path: "x", Content: []byte("y")})
	if p.Empty() {
		t.Fatal("patch with files should not be empty")
	}
	p2 := Patch{Diffs: []DiffText{{Raw: "diff --git a/x b/x"}}}
	if p2.Empty() {
		t.Fatal("patch with diffs should not be empty")
	}
}
