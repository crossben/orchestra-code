// Package fsdiff provides git-free change tracking for directories that are
// not git repositories. Orchestra's supervised loop is built around diffs:
// show what changed, keep it on accept, restore it on reject. Inside a git
// repo those jobs belong to git; everywhere else this package fills in by
// snapshotting the working directory before a run and diffing/restoring
// against that snapshot afterwards.
//
// Diffs are rendered in git's unified format (diff --git / --- +++ / @@), so
// the rest of Orchestra — colored diff rendering, review prompts, TUI panes —
// needs no special-casing.
package fsdiff

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const (
	// MaxFileBytes caps how much of any single file is captured. Larger
	// files are listed but their content is withheld (they diff/restore as
	// opaque entries rather than line-by-line).
	MaxFileBytes = 1 << 20 // 1 MiB
	// MaxTotalBytes is a runaway guard on the whole snapshot.
	maxTotalBytes = 128 << 20 // 128 MiB
)

// ErrTooLarge reports that the directory holds more content than Orchestra
// will snapshot for git-free supervision.
var ErrTooLarge = errors.New("directory too large to snapshot (>128 MiB of file contents)")

// File is one captured file. Content is nil exactly when Withheld is true.
type File struct {
	Mode     os.FileMode
	Content  []byte
	Withheld bool // too large to capture; treated as opaque
}

// Snapshot is the captured state of a directory. Keys are slash-separated,
// repo-relative paths ("src/main.go").
type Snapshot struct {
	Dir   string // absolute directory the snapshot was taken from
	Files map[string]*File
}

// excludedDirs are directory names never walked: VCS internals, dependency
// trees, caches, and build outputs. Matching is by base name.
var excludedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "bower_components": true, "jspm_packages": true,
	"vendor": true, "__pycache__": true, ".tox": true, ".venv": true,
	"venv": true, ".mypy_cache": true, ".pytest_cache": true,
	".gradle": true, ".terraform": true, ".cache": true,
	"dist": true, "build": true, "out": true, "target": true,
	".next": true, ".nuxt": true, "coverage": true,
	".idea": true, ".vscode": true,
}

// excludedFileNames are files never captured, by base name.
var excludedFileNames = map[string]bool{
	".DS_Store": true,
	"Thumbs.db": true,
}

// excludedSuffixes are file suffixes never captured (binaries and archives —
// they diff as opaque "Binary files … differ" entries if they change).
var excludedSuffixes = []string{
	".pyc", ".pyo", ".class", ".o", ".so", ".dylib", ".dll", ".exe",
	".png", ".jpg", ".jpeg", ".gif", ".ico", ".webp", ".pdf", ".zip",
	".tar", ".gz", ".tgz", ".bz2", ".7z", ".rar", ".woff", ".woff2",
	".ttf", ".otf", ".eot", ".mp3", ".mp4", ".mov", ".avi", ".sqlite",
	".db",
}

// Excluded reports whether a path component should be skipped when walking a
// working directory. Exported so other packages (e.g. the API agent's context
// snapshot) walk with identical rules.
func Excluded(rel string, isDir bool) bool {
	base := filepath.Base(rel)
	if isDir {
		return excludedDirs[base]
	}
	if excludedFileNames[base] {
		return true
	}
	for _, suf := range excludedSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// Capture snapshots every regular file under dir, subject to the exclusion
// and size rules above.
func Capture(dir string) (*Snapshot, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	snap := &Snapshot{Dir: abs, Files: map[string]*File{}}
	var total int
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir // unreadable subtree: skip, don't fail
			}
			return nil
		}
		rel, rerr := filepath.Rel(abs, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if Excluded(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil // symlinks and specials are not tracked
		}
		if Excluded(rel, false) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if info.Size() > MaxFileBytes {
			snap.Files[rel] = &File{Mode: info.Mode().Perm(), Withheld: true}
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil // unreadable file: ignore
		}
		total += len(data)
		if total > maxTotalBytes {
			return ErrTooLarge
		}
		snap.Files[rel] = &File{Mode: info.Mode().Perm(), Content: data}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// looksBinary reports whether data starts like a binary blob (NUL byte in the
// first 800 bytes, mirroring git's heuristic).
func looksBinary(data []byte) bool {
	n := len(data)
	if n > 800 {
		n = 800
	}
	return bytes.IndexByte(data[:n], 0) >= 0
}
