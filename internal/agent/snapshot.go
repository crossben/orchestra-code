package agent

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crossben/orchestra-code/internal/fsdiff"
)

// DefaultContextBudget is the default byte cap on the repository snapshot an
// APIAgent sends with every task. CLI agents gather their own context; an API
// agent cannot, so Orchestra ships one to it.
const DefaultContextBudget = 96 << 10 // 96 KiB

// snapshotSniff is how many leading bytes are inspected to detect binaries.
const snapshotSniff = 800

// snapshotSkipNames are lockfiles and generated artifacts that burn budget
// without helping a model understand the code.
var snapshotSkipNames = map[string]bool{
	"go.sum":            true,
	"package-lock.json": true,
	"yarn.lock":         true,
	"pnpm-lock.yaml":    true,
	"Cargo.lock":        true,
	"poetry.lock":       true,
	"Gemfile.lock":      true,
}

type snapFile struct {
	rel string // slash-separated, repo-relative
	abs string
}

// buildSnapshot renders a bounded textual view of the repository at dir: an
// annotated file list plus the contents of text files up to budget bytes.
// Binary files and lockfiles are listed but never inlined; anything that does
// not fit is explicitly marked so the model knows what it did not see.
func buildSnapshot(dir string, budget int) (string, error) {
	if budget <= 0 {
		budget = DefaultContextBudget
	}
	files, err := snapList(dir)
	if err != nil {
		return "", err
	}

	var contents strings.Builder
	type note struct{ rel, tag string }
	var notes []note
	remaining := budget
	shown := 0
	for _, f := range files {
		switch {
		case snapshotSkipNames[f.rel]:
			notes = append(notes, note{f.rel, "skipped (lockfile)"})
			continue
		case snapBinary(f.abs):
			notes = append(notes, note{f.rel, "skipped (binary)"})
			continue
		}
		data, err := os.ReadFile(f.abs)
		if err != nil {
			notes = append(notes, note{f.rel, "unreadable"})
			continue
		}
		if len(data) == 0 {
			notes = append(notes, note{f.rel, "empty"})
			continue
		}
		if remaining <= 0 {
			notes = append(notes, note{f.rel, "not shown (budget exhausted)"})
			continue
		}
		fmt.Fprintf(&contents, "----- %s -----\n", f.rel)
		if len(data) > remaining {
			fmt.Fprintf(&contents, "%s\n[truncated at %d of %d bytes]\n\n",
				safeText(data[:remaining]), remaining, len(data))
			remaining = 0
		} else {
			contents.Write(data)
			if data[len(data)-1] != '\n' {
				contents.WriteByte('\n')
			}
			contents.WriteByte('\n')
			remaining -= len(data)
		}
		shown++
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Repository snapshot: %d files total, %d shown below, up to %d bytes of contents:\n\n",
		len(files), shown, budget)
	b.WriteString("=== FILE LIST ===\n")
	for _, f := range files {
		b.WriteString(f.rel)
		b.WriteString("\n")
	}
	if len(notes) > 0 {
		b.WriteString("\n=== NOTES ===\n")
		for _, n := range notes {
			fmt.Fprintf(&b, "%s: %s\n", n.rel, n.tag)
		}
	}
	b.WriteString("\n=== CONTENTS ===\n")
	b.WriteString(contents.String())
	return b.String(), nil
}

// snapList enumerates candidate files. Inside a git repository it uses
// git (tracked + untracked-not-ignored, so .gitignore keeps vendor dirs and
// secrets out automatically); anywhere else it falls back to walking the
// directory with Orchestra's standard exclusion rules.
func snapList(dir string) ([]snapFile, error) {
	if files, err := gitLsFiles(dir); err == nil {
		return files, nil
	}
	return walkFiles(dir)
}

// gitLsFiles lists candidates via git; it fails outside a repository or when
// git is unavailable.
func gitLsFiles(dir string) ([]snapFile, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git not available")
	}
	cmd := exec.Command("git", "-C", dir, "ls-files", "-co", "--exclude-standard")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("snapshot: git ls-files: %w", err)
	}
	return parseFileLines(out.String(), dir), nil
}

// walkFiles is the git-free fallback: a plain directory walk that skips VCS
// internals, dependency trees, caches, and binaries via fsdiff's rules.
func walkFiles(dir string) ([]snapFile, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var files []snapFile
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, werr error) error {
		if werr != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if fsdiff.Excluded(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || fsdiff.Excluded(rel, false) {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil && info.Size() > maxWalkFileBytes {
			return nil // too big to be useful as model context
		}
		files = append(files, snapFile{rel: rel, abs: p})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("snapshot: walk %s: %w", root, err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].rel < files[j].rel })
	return files, nil
}

// maxWalkFileBytes caps individual file reads during the fallback walk.
const maxWalkFileBytes = 8 << 20 // 8 MiB

// parseFileLines converts git ls-files output into sorted snapFile entries.
func parseFileLines(out string, dir string) []snapFile {
	lines := strings.Split(out, "\n")
	sort.Strings(lines)
	files := make([]snapFile, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		files = append(files, snapFile{
			rel: filepath.ToSlash(l),
			abs: filepath.Join(dir, filepath.FromSlash(l)),
		})
	}
	return files
}

// snapBinary reports whether the file looks binary (NUL byte in its first
// snapshotSniff bytes). Missing/unreadable files are treated as binary so they
// are never inlined.
func snapBinary(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	buf := make([]byte, snapshotSniff)
	n, _ := f.Read(buf)
	return bytes.IndexByte(buf[:n], 0) >= 0
}

// safeText coerces arbitrary bytes into printable text (control characters
// except newline/tab become spaces), so a misdetected "text" file can't inject
// garbage into the prompt.
func safeText(b []byte) string {
	var sb strings.Builder
	sb.Grow(len(b))
	for _, c := range b {
		if c == '\n' || c == '\t' || (c >= 0x20 && c < 0x7f) || c >= 0x80 {
			sb.WriteByte(c)
			continue
		}
		sb.WriteByte(' ')
	}
	return sb.String()
}
