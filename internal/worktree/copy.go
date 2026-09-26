package worktree

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/crossben/orchestra-code/internal/fsdiff"
	"github.com/crossben/orchestra-code/internal/gitutil"
)

// CopyIsolator isolates tasks in plain directory copies, for working
// directories that are not git repositories. Each task gets a full copy of the
// source (minus the directories fsdiff never tracks); its changes are diffed
// with fsdiff against the state it started from, and merged back file by file
// only if none of the touched files changed in the source meanwhile.
type CopyIsolator struct {
	src  string // absolute source directory
	root string // temp dir holding the copies

	mu    sync.Mutex
	trees map[string]*copyTree // by tree ID
}

// copyTree is the bookkeeping for one copy.
type copyTree struct {
	dir      string
	baseline *fsdiff.Snapshot // fsdiff capture of the fresh copy (diff baseline)
	sigs     map[string]string
}

// NewCopy prepares a copy root for src.
func NewCopy(src string) (*CopyIsolator, error) {
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	root, err := os.MkdirTemp("", "orchestra-cp-")
	if err != nil {
		return nil, err
	}
	// The headless engine treats a git work tree as "commit here"; a copy
	// that happened to land inside one would commit into a stranger's repo.
	if gitutil.IsRepo(root) {
		_ = os.RemoveAll(root)
		return nil, fmt.Errorf("temp directory %s is inside a git repository; set TMPDIR elsewhere", filepath.Dir(root))
	}
	return &CopyIsolator{src: abs, root: root, trees: map[string]*copyTree{}}, nil
}

// Add copies the source's current state into a fresh directory for task id.
// fromRef is ignored (there are no refs outside git).
func (c *CopyIsolator) Add(id, _ string) (Tree, error) {
	if !safeRel(id) || strings.Contains(id, "/") {
		return Tree{}, fmt.Errorf("invalid task id %q", id)
	}
	dir := filepath.Join(c.root, id)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return Tree{}, fmt.Errorf("create copy %s: %w", id, err)
	}
	sigs, err := copyTreeFiles(c.src, dir, c.root)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Tree{}, fmt.Errorf("copy %s: %w", id, err)
	}
	base, err := fsdiff.Capture(dir)
	if err != nil {
		_ = os.RemoveAll(dir)
		return Tree{}, fmt.Errorf("snapshot copy %s: %w", id, err)
	}
	c.mu.Lock()
	c.trees[id] = &copyTree{dir: dir, baseline: base, sigs: sigs}
	c.mu.Unlock()
	return Tree{ID: id, Dir: dir}, nil
}

func (c *CopyIsolator) tree(t Tree) (*copyTree, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ct, ok := c.trees[t.ID]
	if !ok || ct.dir != t.Dir {
		return nil, fmt.Errorf("unknown copy %q", t.ID)
	}
	return ct, nil
}

// Diff returns the copy's changes since it was created, in git's unified format.
func (c *CopyIsolator) Diff(t Tree) (string, error) {
	ct, err := c.tree(t)
	if err != nil {
		return "", err
	}
	return fsdiff.Diff(ct.dir, ct.baseline)
}

// DiffStat counts files and +/- lines in Diff's output.
func (c *CopyIsolator) DiffStat(t Tree) (files, added, removed int, err error) {
	d, err := c.Diff(t)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, line := range strings.Split(d, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			files++
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return files, added, removed, nil
}

// Merge applies the copy's added/modified/deleted files to the source. Every
// touched path is checked first: it must be a safe relative path that does
// not pass through a symlink in the source (error otherwise), and its current
// state in the source must equal the state the copy started from (conflict
// otherwise). Only when all checks pass is anything written.
func (c *CopyIsolator) Merge(t Tree, _ string) (conflict bool, err error) {
	ct, err := c.tree(t)
	if err != nil {
		return false, err
	}
	now, err := trackedSigs(ct.dir)
	if err != nil {
		return false, fmt.Errorf("scan copy %s: %w", t.ID, err)
	}

	var touched []string
	for p, sig := range now {
		if ct.sigs[p] != sig {
			touched = append(touched, p) // added or modified
		}
	}
	for p, sig := range ct.sigs {
		if _, ok := now[p]; !ok && isFileSig(sig) && fsdiffTracks(p) {
			touched = append(touched, p) // deleted
		}
	}
	sort.Strings(touched)

	// 1. Guard every path before looking at or writing anything.
	for _, p := range touched {
		if err := c.guard(p); err != nil {
			return false, err
		}
	}
	// 2. Conflict check: the source must still hold the baseline version.
	for _, p := range touched {
		cur, err := sigOf(filepath.Join(c.src, filepath.FromSlash(p)))
		if err != nil {
			return false, err
		}
		if cur != ct.sigs[p] {
			return true, nil
		}
	}
	// 3. Apply.
	for _, p := range touched {
		target := filepath.Join(c.src, filepath.FromSlash(p))
		if _, ok := now[p]; !ok {
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				return false, fmt.Errorf("remove %s: %w", p, err)
			}
			pruneEmpty(c.src, filepath.Dir(target))
			continue
		}
		if err := copyFileAtomic(filepath.Join(ct.dir, filepath.FromSlash(p)), target); err != nil {
			return false, fmt.Errorf("write %s: %w", p, err)
		}
	}
	return false, nil
}

// guard rejects paths that could write outside the source directory: anything
// not clean and relative, or whose existing parent directories in the source
// include a symlink.
func (c *CopyIsolator) guard(p string) error {
	if !safeRel(p) {
		return fmt.Errorf("refusing unsafe path %q", p)
	}
	parts := strings.Split(p, "/")
	cur := c.src
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			return nil // the rest will be created as real directories
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("refusing to write %q: %s is not a plain directory in the source", p, cur)
		}
	}
	return nil
}

// Remove deletes one copy.
func (c *CopyIsolator) Remove(t Tree) error {
	c.mu.Lock()
	delete(c.trees, t.ID)
	c.mu.Unlock()
	if t.Dir == "" || !strings.HasPrefix(t.Dir, c.root+string(filepath.Separator)) {
		return nil
	}
	return os.RemoveAll(t.Dir)
}

// Cleanup deletes every copy and the root.
func (c *CopyIsolator) Cleanup() {
	c.mu.Lock()
	c.trees = map[string]*copyTree{}
	c.mu.Unlock()
	_ = os.RemoveAll(c.root)
}

// safeRel reports whether p is a clean, slash-separated relative path that
// stays inside its root.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	if path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../") {
		return false
	}
	return true
}

// fsdiffTracks reports whether fsdiff would show p in a diff: no path
// component is an excluded directory and the file itself is not excluded.
func fsdiffTracks(p string) bool {
	parts := strings.Split(p, "/")
	for i := range parts[:len(parts)-1] {
		if fsdiff.Excluded(strings.Join(parts[:i+1], "/"), true) {
			return false
		}
	}
	return !fsdiff.Excluded(p, false)
}

// Signatures: "f<mode>:<sha256>" for regular files, "l:<target>" for symlinks,
// "" for absent paths.
func isFileSig(s string) bool { return strings.HasPrefix(s, "f") }

func sigOf(p string) (string, error) {
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(p)
		if err != nil {
			return "", err
		}
		return "l:" + target, nil
	case info.Mode().IsRegular():
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "", err
		}
		return fmt.Sprintf("f%o:%s", info.Mode().Perm(), hex.EncodeToString(h.Sum(nil))), nil
	default:
		return "d", nil // a directory or special file where a file is expected
	}
}

// trackedSigs signs every regular file in dir that fsdiff tracks (the files a
// reviewer can see in the diff) — the only files Merge will ever touch.
func trackedSigs(dir string) (map[string]string, error) {
	sigs := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
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
		s, err := sigOf(p)
		if err != nil {
			return err
		}
		sigs[rel] = s
		return nil
	})
	return sigs, err
}

// copyTreeFiles copies src into dst (which exists), skipping directories fsdiff
// excludes (and skip, the copy root, should it live inside src), and returns
// the signature of every copied file and symlink.
func copyTreeFiles(src, dst, skip string) (map[string]string, error) {
	sigs := map[string]string{}
	var dirModes []struct {
		path string
		mode os.FileMode
	}
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		slash := filepath.ToSlash(rel)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			if p == skip || fsdiff.Excluded(slash, true) {
				return filepath.SkipDir
			}
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			dirModes = append(dirModes, struct {
				path string
				mode os.FileMode
			}{target, info.Mode().Perm() | 0o700})
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if err := os.Symlink(link, target); err != nil {
				return err
			}
			sigs[slash] = "l:" + link
		case info.Mode().IsRegular():
			s, err := copyFileSig(p, target, info.Mode().Perm())
			if err != nil {
				return err
			}
			sigs[slash] = s
		}
		return nil // special files are skipped
	})
	if err != nil {
		return nil, err
	}
	for i := len(dirModes) - 1; i >= 0; i-- {
		_ = os.Chmod(dirModes[i].path, dirModes[i].mode)
	}
	return sigs, nil
}

// copyFileSig copies one regular file (content + mode), returning the
// signature of exactly the bytes copied.
func copyFileSig(src, dst string, mode os.FileMode) (string, error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(out, h), in); err != nil {
		out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(dst, mode); err != nil { // defeat umask
		return "", err
	}
	return fmt.Sprintf("f%o:%s", mode, hex.EncodeToString(h.Sum(nil))), nil
}

// copyFileAtomic writes src's content and mode to dst via a temp file in dst's
// directory and a rename, creating parent directories as needed.
func copyFileAtomic(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".orchestra-merge-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, info.Mode().Perm()); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, dst); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// pruneEmpty removes dir and its parents while they are empty, stopping at root.
func pruneEmpty(root, dir string) {
	for dir != root && strings.HasPrefix(dir, root+string(filepath.Separator)) {
		if err := os.Remove(dir); err != nil {
			return // not empty (or not removable): stop
		}
		dir = filepath.Dir(dir)
	}
}
