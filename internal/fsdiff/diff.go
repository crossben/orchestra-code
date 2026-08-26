package fsdiff

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Diff renders a git-style unified diff between the snapshot and the current
// contents of dir. Files that are byte-identical produce no output; added,
// modified, deleted, and opaque (binary/withheld) files each get a section in
// git's format so existing rendering/coloring works unchanged.
func Diff(dir string, before *Snapshot) (string, error) {
	after, err := Capture(dir)
	if err != nil {
		return "", err
	}
	if before == nil {
		before = &Snapshot{Files: map[string]*File{}}
	}

	paths := make([]string, 0, len(before.Files)+len(after.Files))
	seen := map[string]bool{}
	for p := range before.Files {
		paths = append(paths, p)
		seen[p] = true
	}
	for p := range after.Files {
		if !seen[p] {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)

	var b strings.Builder
	for _, p := range paths {
		o, hadO := before.Files[p]
		c, hadC := after.Files[p]
		switch {
		case hadO && !hadC:
			writeRemoved(&b, p, o)
		case !hadO && hadC:
			writeAdded(&b, p, c)
		case o.Withheld || c.Withheld:
			writeOpaque(&b, p) // content unknown on at least one side
		case bytes.Equal(o.Content, c.Content):
			// unchanged (mode-only changes are not tracked git-free)
		default:
			writeModified(&b, p, o, c)
		}
	}
	return b.String(), nil
}

// fileMode renders a permission set the way git does: regular files are
// either 100644 or 100755 (git tracks only the executable bit).
func fileMode(f *File) string {
	if f.Mode&0o111 != 0 {
		return "100755"
	}
	return "100644"
}

func writeHeader(b *strings.Builder, path string, f *File, kind string) {
	fmt.Fprintf(b, "diff --git a/%s b/%s\n", path, path)
	switch kind {
	case "added":
		fmt.Fprintf(b, "new file mode %s\n", fileMode(f))
	case "deleted":
		fmt.Fprintf(b, "deleted file mode %s\n", fileMode(f))
	}
}

func writeAdded(b *strings.Builder, path string, f *File) {
	if f.Withheld || looksBinary(f.Content) {
		writeHeader(b, path, f, "added")
		fmt.Fprintln(b, "Binary files /dev/null and b/"+path+" differ")
		return
	}
	lines, nl := splitLines(f.Content)
	writeHeader(b, path, f, "added")
	fmt.Fprintf(b, "--- /dev/null\n+++ b/%s\n", path)
	edits := insAll(lines)
	writeHunks(b, nil, lines, edits, true, nl)
}

func writeRemoved(b *strings.Builder, path string, f *File) {
	if f.Withheld || looksBinary(f.Content) {
		writeHeader(b, path, f, "deleted")
		fmt.Fprintln(b, "Binary files a/"+path+" and /dev/null differ")
		return
	}
	lines, nl := splitLines(f.Content)
	writeHeader(b, path, f, "deleted")
	fmt.Fprintf(b, "--- a/%s\n+++ /dev/null\n", path)
	edits := delAll(lines)
	writeHunks(b, lines, nil, edits, nl, true)
}

func writeOpaque(b *strings.Builder, path string) {
	fmt.Fprintf(b, "diff --git a/%s b/%s\nBinary files a/%s and b/%s differ\n", path, path, path, path)
}

func writeModified(b *strings.Builder, path string, o, c *File) {
	if looksBinary(o.Content) || looksBinary(c.Content) {
		fmt.Fprintf(b, "diff --git a/%s b/%s\nBinary files a/%s and b/%s differ\n", path, path, path, path)
		return
	}
	aLines, aNL := splitLines(o.Content)
	bLines, bNL := splitLines(c.Content)
	writeHeader(b, path, c, "")
	fmt.Fprintf(b, "--- a/%s\n+++ b/%s\n", path, path)
	writeHunks(b, aLines, bLines, lineEdits(aLines, bLines), aNL, bNL)
}

// ctxLines is how many unchanged lines surround each change cluster,
// matching git's default.
const ctxLines = 3

// writeHunks renders edits as unified-diff hunks: changes closer than 2×ctx
// share a hunk, each hunk carries an @@ header, and missing trailing newlines
// get git's "\ No newline at end of file" marker.
func writeHunks(b *strings.Builder, a, bb []string, edits []edit, aNL, bNL bool) {
	n := len(edits)

	// Mark included edits: every change plus up to ctxLines of context
	// around it. Runs of context bridge nearby changes into one hunk.
	include := make([]bool, n)
	countdown := 0
	for i, e := range edits {
		if e.kind != opEq {
			include[i] = true
			countdown = ctxLines
			continue
		}
		if countdown > 0 {
			include[i] = true
			countdown--
		}
	}
	countdown = 0
	for i := n - 1; i >= 0; i-- {
		if edits[i].kind != opEq {
			include[i] = true
			countdown = ctxLines
			continue
		}
		if countdown > 0 {
			include[i] = true
			countdown--
		}
	}

	// Emit contiguous included blocks as hunks.
	posA, posB := 0, 0
	for s := 0; s < n; {
		if !include[s] {
			advance(&posA, &posB, edits[s])
			s++
			continue
		}
		e := s
		for e < n && include[e] {
			e++
		}
		writeOneHunk(b, a, bb, edits[s:e], &posA, &posB, aNL, bNL)
		s = e
	}
}

// writeOneHunk emits the @@ header and body for one contiguous run of edits,
// updating the running line positions past the whole run.
func writeOneHunk(b *strings.Builder, a, bb []string, run []edit, posA, posB *int, aNL, bNL bool) {
	aStart, bStart := *posA, *posB
	var aCount, bCount int
	for _, e := range run {
		switch e.kind {
		case opDel:
			aCount++
		case opIns:
			bCount++
		default:
			aCount++
			bCount++
		}
	}
	fmt.Fprintf(b, "@@ -%s +%s @@\n", rng(aStart, aCount), rng(bStart, bCount))

	for _, e := range run {
		switch e.kind {
		case opEq:
			b.WriteString(" " + e.line + "\n")
			*posA++
			*posB++
		case opDel:
			b.WriteString("-" + e.line + "\n")
			if *posA == len(a)-1 && !aNL {
				b.WriteString("\\ No newline at end of file\n")
			}
			*posA++
		case opIns:
			b.WriteString("+" + e.line + "\n")
			if *posB == len(bb)-1 && !bNL {
				b.WriteString("\\ No newline at end of file\n")
			}
			*posB++
		}
	}
}

// advance moves the line cursors past one edit without emitting it.
func advance(posA, posB *int, e edit) {
	switch e.kind {
	case opDel:
		*posA++
	case opIns:
		*posB++
	default:
		*posA++
		*posB++
	}
}

// rng formats one side of an @@ header. git omits ",1" for single-line spans
// and writes the insertion point followed by ",0" for empty ones.
func rng(start, count int) string {
	switch {
	case count == 0:
		return fmt.Sprintf("%d,0", start)
	case count == 1:
		return strconv.Itoa(start + 1)
	default:
		return fmt.Sprintf("%d,%d", start+1, count)
	}
}
