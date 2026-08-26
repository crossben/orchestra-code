// Package patch turns free-form model output into changes on disk. An
// API-backed agent cannot edit files itself, so it describes them — as a
// unified diff and/or whole-file blocks — and this package extracts those
// changes leniently (models add prose around the payload) and applies them
// safely inside the working tree.
//
// Safety rules:
//   - Unified diffs are validated with `git apply --check` before anything is
//     written, so a conflicting diff leaves the tree byte-identical.
//   - Whole-file writes happen only after every target path has been validated;
//     paths outside the working directory are rejected.
package patch

// FileWrite is one whole-file create/update/delete.
type FileWrite struct {
	Path    string // repo-relative slash path, cleaned
	Content []byte // ignored when Delete
	Delete  bool
}

// DiffText is one raw unified diff to apply with git.
type DiffText struct{ Raw string }

// Patch is everything extractable from one model reply.
type Patch struct {
	Diffs []DiffText
	Files []FileWrite
}

// Empty reports whether the patch would change nothing.
func (p Patch) Empty() bool { return len(p.Diffs) == 0 && len(p.Files) == 0 }
