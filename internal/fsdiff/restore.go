package fsdiff

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Restore reverts dir to the state captured in before: files created since the
// snapshot are deleted (along with parent dirs left empty), modified files are
// rewritten, deleted files are recreated, and modes are restored. Files whose
// baseline content was withheld (too large) are left untouched — there is
// nothing to restore them to.
func Restore(dir string, before *Snapshot) error {
	root := before.Dir
	after, err := Capture(root)
	if err != nil {
		return fmt.Errorf("re-snapshot for restore: %w", err)
	}

	// 1. Delete files that did not exist at snapshot time.
	var emptiedDirs = map[string]bool{}
	for p := range after.Files {
		if _, existed := before.Files[p]; existed {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(p))
		if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", p, err)
		}
		for d := filepath.Dir(target); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
			emptiedDirs[d] = true
		}
	}

	// 2. Bring every baseline file back (content + mode).
	for p, f := range before.Files {
		if f.Withheld {
			continue // unknown baseline content: nothing to restore
		}
		cur, exists := after.Files[p]
		sameContent := exists && cur != nil && !cur.Withheld && bytes.Equal(cur.Content, f.Content)
		if sameContent && cur.Mode == f.Mode {
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(p))
		if sameContent {
			if err := os.Chmod(target, f.Mode.Perm()); err != nil {
				return fmt.Errorf("chmod %s: %w", p, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", p, err)
		}
		if err := os.WriteFile(target, f.Content, f.Mode.Perm()); err != nil {
			return fmt.Errorf("restore %s: %w", p, err)
		}
	}

	// 3. Prune directories emptied by step 1 (deepest first; Remove is a
	// no-op on non-empty dirs).
	dirs := make([]string, 0, len(emptiedDirs))
	for d := range emptiedDirs {
		dirs = append(dirs, d)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	return nil
}
