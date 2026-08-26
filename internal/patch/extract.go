package patch

import (
	"path"
	"strings"
)

const deleteMarker = "<DELETE>"

// Extract leniently pulls changes out of free-form model text. It understands,
// in order of preference:
//
//  1. Fenced blocks containing a unified diff (```diff … ``` or unlabeled):
//     collected verbatim for `git apply`.
//  2. Fenced blocks whose FIRST line is a repo-relative file path and whose
//     remainder is the full new file content:
//
//     ```main.go
//     package main
//     ...
//     ```
//
//     A body of exactly "<DELETE>" marks a deletion.
//  3. An unfenced unified diff pasted anywhere in the reply.
//
// It returns false when no changes could be extracted (a plain prose answer).
func Extract(text string) (Patch, bool) {
	var p Patch
	p.Diffs, p.Files = extractFenced(text)
	if len(p.Diffs) == 0 {
		if raw := extractUnfencedDiff(text); raw != "" {
			p.Diffs = append(p.Diffs, DiffText{Raw: raw})
		}
	}
	return p, !p.Empty()
}

// extractFenced scans ```-delimited blocks, classifying each by shape.
func extractFenced(text string) (diffs []DiffText, files []FileWrite) {
	lines := strings.Split(text, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			continue
		}
		// Collect until a closing fence.
		var body []string
		closed := false
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				i = j
				closed = true
				break
			}
			body = append(body, lines[j])
		}
		if !closed || len(body) == 0 {
			break // unterminated fence: stop scanning, don't guess
		}
		content := strings.Join(body, "\n")

		switch classifyBlock(body) {
		case classDiff:
			diffs = append(diffs, DiffText{Raw: content})
		case classFile:
			f, ok := parseFileBlock(body)
			if ok {
				files = append(files, f)
			}
		}
	}
	return diffs, files
}

type blockClass int

const (
	classOther blockClass = iota
	classDiff
	classFile
)

// classifyBlock decides what a fenced block holds by its first non-empty line.
func classifyBlock(body []string) blockClass {
	first := ""
	for _, l := range body {
		t := strings.TrimSpace(l)
		if t != "" {
			first = t
			break
		}
	}
	if first == "" {
		return classOther
	}
	if looksLikeDiffStart(first) {
		return classDiff
	}
	return classFile // tentatively; parseFileBlock may still reject it
}

// looksLikeDiffStart recognizes the shapes a unified diff can begin with.
func looksLikeDiffStart(line string) bool {
	for _, pre := range []string{"diff --git", "diff -", "--- a/", "--- /dev/null", "--- ", "Index: ", "*** ", "@@ -"} {
		if strings.HasPrefix(line, pre) {
			return true
		}
	}
	return false
}

// parseFileBlock interprets [path, ...content] bodies.
func parseFileBlock(body []string) (FileWrite, bool) {
	if len(body) < 1 {
		return FileWrite{}, false
	}
	p, ok := cleanRelPath(body[0])
	if !ok {
		return FileWrite{}, false
	}
	rest := body[1:]
	// Trim a single leading blank separator line between path and content.
	if len(rest) > 0 && strings.TrimSpace(rest[0]) == "" {
		rest = rest[1:]
	}
	content := strings.Join(rest, "\n")
	if strings.TrimSpace(content) == deleteMarker {
		return FileWrite{Path: p, Delete: true}, true
	}
	return FileWrite{Path: p, Content: []byte(content)}, true
}

// extractUnfencedDiff grabs a raw unified diff pasted without a fence: from the
// first diff-shaped line to the end of the text (lenient — trailing prose after
// a hunk is rare, and `git apply --check` guards correctness anyway).
func extractUnfencedDiff(text string) string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "diff --git ") || strings.HasPrefix(t, "--- a/") || strings.HasPrefix(t, "@@ -") {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	return strings.Join(lines[start:], "\n")
}

// cleanRelPath accepts only safe repo-relative slash paths and returns their
// normalized form ("./a/b.go" → "a/b.go"). Any ".." component is rejected
// outright (even when it would clean away) so model output can never climb.
func cleanRelPath(p string) (string, bool) {
	p = strings.TrimSpace(p)
	if p == "" || strings.ContainsAny(p, " \t\\") {
		return "", false
	}
	p = strings.TrimPrefix(p, "./")
	if path.IsAbs(p) {
		return "", false
	}
	for _, part := range strings.Split(p, "/") {
		if part == ".." {
			return "", false
		}
	}
	c := path.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}
