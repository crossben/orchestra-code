package patch

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Apply applies p inside dir.
//
// Ordering guarantees:
//  1. Every unified diff is first validated with `git apply --check`; if any
//     diff fails, nothing has been written (the tree stays byte-identical).
//  2. Every whole-file target path is validated before any write happens.
//  3. Diffs are applied, then deletions run, then creations/updates are
//     written via temp-file+rename so a crash never leaves half a file.
//
// dir must be inside a git repository for diffs to apply; whole-file writes
// also work in plain directories.
func Apply(ctx context.Context, dir string, p Patch) error {
	// Normalize diffs: git requires each patch to end with a newline, and
	// model-extracted fences often drop it.
	diffs := make([]string, len(p.Diffs))
	for i := range p.Diffs {
		raw := p.Diffs[i].Raw
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if !strings.HasSuffix(raw, "\n") {
			raw += "\n"
		}
		diffs[i] = raw
	}

	// 1. Dry-run every diff first: atomicity across the whole patch.
	for i, raw := range diffs {
		if raw == "" {
			continue
		}
		out, err := runGit(ctx, dir, raw, "apply", "--check", "--whitespace=nowarn", "-")
		if err != nil {
			return fmt.Errorf("diff #%d does not apply (nothing was written): %s", i+1, oneLine(out))
		}
	}

	// 2. Validate all file targets up front so a bad path can't produce a
	// partial application after the diffs have already landed.
	files := make([]FileWrite, len(p.Files))
	for i, f := range p.Files {
		rel, ok := cleanRelPath(f.Path)
		if !ok {
			return fmt.Errorf("invalid file path %q", f.Path)
		}
		f.Path = rel
		files[i] = f
	}

	// 3. Apply the diffs for real.
	for i, raw := range diffs {
		if raw == "" {
			continue
		}
		out, err := runGit(ctx, dir, raw, "apply", "--whitespace=nowarn", "-")
		if err != nil {
			return fmt.Errorf("apply diff #%d: %s", i+1, oneLine(out))
		}
	}

	// 4a. Deletions first, so a write may legitimately recreate the same path.
	for _, f := range files {
		if !f.Delete {
			continue
		}
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(f.Path))); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("delete %s: %w", f.Path, err)
		}
	}

	// 4b. Creations/updates via temp file + rename.
	for _, f := range files {
		if f.Delete {
			continue
		}
		if err := writeFileAtomic(dir, f.Path, f.Content); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic writes content to dir/path via a temp file in the same
// directory followed by a rename.
func writeFileAtomic(dir, relPath string, content []byte) error {
	abs := filepath.Join(dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create parent dir for %s: %w", relPath, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(abs), ".orchestra-patch-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	name := tmp.Name()
	defer func() {
		os.Remove(name) // no-op once renamed away
	}()
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	if err := os.Rename(name, abs); err != nil {
		return fmt.Errorf("write %s: %w", relPath, err)
	}
	return nil
}

// runGit executes git in dir with stdin wired to diffText, returning combined
// output (used for `git apply -`, which reads the diff from stdin).
func runGit(ctx context.Context, dir, diffText string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(diffText)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

// oneLine collapses output to a single short line for error messages.
func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", "; "))
	const max = 300
	r := []rune(s)
	if len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return s
}
