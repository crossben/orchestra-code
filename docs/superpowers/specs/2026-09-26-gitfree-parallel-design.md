# Git-free `do --parallel` and `benchmark` — design

Date: 2026-09-26 · Scope: `internal/worktree`, `internal/engine` (headless path), `cmd/orchestra/do.go`,
`cmd/orchestra/benchmark.go`.

## Intent

Single runs already work in plain folders (`internal/fsdiff`: snapshot → diff → restore). Parallel `do`
and `benchmark` still refuse to run outside a git repository because they isolate each task in a git
worktree. This change gives them a git-free isolation backend so both work anywhere, while keeping the
behaviour inside a repository byte-for-byte the same.

## Interface

`internal/worktree` gains an `Isolator` interface. The existing git `*Manager` satisfies it unchanged; a
new `*CopyIsolator` implements it for plain folders. `New(dir)` picks one automatically.

```go
type Isolator interface {
    Add(id, fromRef string) (Tree, error)          // fromRef is ignored by the copy isolator
    Diff(t Tree) (string, error)                   // git-style unified diff of the task's own changes
    DiffStat(t Tree) (files, added, removed int, err error)
    Merge(t Tree, message string) (conflict bool, err error)
    Remove(t Tree) error
    Cleanup()
}

func New(dir string) (Isolator, error)            // git repo → *Manager, otherwise → *CopyIsolator
func NewCopy(dir string) (*CopyIsolator, error)
```

`Tree` is unchanged (`ID`, `Dir`, `Branch`); the copy isolator leaves `Branch` empty.

## Copy isolator semantics

- **Root.** `os.MkdirTemp("", "orchestra-cp-")`, one sub-directory per task. `NewCopy` refuses a temp
  root that is itself inside a git work tree (otherwise the headless engine would see a "repo" and
  commit into it).
- **Add (copy).** Walk the source directory at `Add` time — so a later dependency wave starts from the
  source *after* earlier merges, like `git worktree add HEAD`. Directories matched by
  `fsdiff.Excluded(rel, true)` (VCS internals, `node_modules`, `vendor`, build outputs, …) are skipped —
  the same rule fsdiff uses for snapshots; no new ignore list. Every other entry is copied: regular files
  with their permission bits, symlinks re-created as symlinks (target copied verbatim, never followed),
  directories with their permission bits (plus owner rwx so the copy stays removable). Special files are
  skipped.
  While copying, a per-file *signature* (mode + SHA-256 of content, or `symlink → target`) is recorded:
  this is the task's merge baseline. Right after copying, `fsdiff.Capture(copy)` is taken as the task's
  diff baseline. Both describe exactly what the agent started from.
- **Diff.** `fsdiff.Diff(copy, baseline)` — the same git-style unified output single runs produce, so
  review prompts, colouring and the TUI need nothing new. `DiffStat` counts `diff --git` sections and
  `+`/`-` body lines of that diff.
- **Merge (accept).** The touched set is every path fsdiff tracks (regular, non-excluded files) whose
  signature in the copy differs from the baseline: added, modified (content or mode) or deleted. Only
  paths the reviewer could see in the diff are merged; symlinks and excluded files never are.
  1. *Guard* every touched path: relative, clean, no `..`, not absolute, and no existing parent component
     in the source may be a symlink (a write can never escape the source directory).
  2. *Conflict check*: for each touched path the source's **current** signature must equal the baseline
     signature (a path absent at baseline must still be absent). Any mismatch — typically an earlier
     accepted task in the same wave touching the same file — reports `conflict=true` and **nothing is
     written for this task** (all-or-nothing per task). Unlike git, there is no line-level three-way
     merge: two tasks editing different hunks of one file conflict.
  3. *Apply*: added/modified files are written via temp file + rename with the copy's mode; deleted files
     are removed and directories left empty by a deletion are pruned.
- **Remove / Cleanup.** `Remove` deletes the task's copy; `Cleanup` removes the whole root. Callers
  `defer Cleanup()`, so ctrl+c (context cancellation) and error paths clean up exactly as worktrees do.

## Engine

`ExecuteHeadless` commits inside a worktree. In a directory that is not a git repository it now snapshots
the directory before the loop (as `Execute` already does) and computes the diff with fsdiff; there is no
commit — the changes stay in the isolated copy until `Merge`. Inside a repository nothing changes.

## Commands

- `do --parallel`: the "needs a git repository" error is removed. In a repo the clean-tree check and the
  stray-write guard (`git restore` of the base) are unchanged. In a plain folder the stray-write guard
  snapshots the base before each wave and restores it with `fsdiff.Restore` if an agent wrote outside
  its copy.
- `benchmark` (and `--compare`): the git requirement is removed; each agent gets its own copy; the
  leaderboard uses `DiffStat`; "merge the winner" uses `Merge`. Memory recording is unchanged.
- Still git-only: nothing else in these commands.

## Supervised loop

Unchanged in shape: agents work in isolation, every result is shown as a diff, and only an explicit
accept merges it into the source directory. The one semantic difference in plain folders is the stricter
per-file conflict rule above.

## Testing

All offline, fake agents (shell scripts / in-process), temp dirs that are not repositories.

- `internal/worktree`: copy fidelity (modes, nested dirs, symlink kept as symlink, excluded dir skipped);
  diff for add/modify/delete; merge success; conflict → nothing written; traversal guard (symlinked
  parent in the source); `Remove`/`Cleanup` delete the copies; `New` picks the right backend.
- `internal/engine`: `ExecuteHeadless` in a plain directory reports the diff and makes no commit.
- `cmd/orchestra`: `runParallel` in a plain temp dir with two shell-script agents writing different files
  → both merged; both writing the same file → the second is reported as a conflict and the source keeps
  the first; benchmark flow in a plain folder with two fake agents ranks and merges the winner.
