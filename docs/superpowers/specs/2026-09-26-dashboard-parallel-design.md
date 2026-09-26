# Parallel runs in the dashboard — design

Date: 2026-09-26 · Base: `feat/dashboard-ui` · Scope: `internal/tui` (Chat tab only), a new shared
package `internal/parallel` extracted from `cmd/orchestra/do.go`, README.

## Intent

`orchestra do --parallel` already plans a request into dependent steps, runs each dependency wave
concurrently in isolated trees and reviews + merges each result. The dashboard can only run one task at a
time. Bring the parallel workflow into Chat, with a live per-task view and the existing file-by-file
review, without a second implementation of the wave logic.

Non-goals: changing `do --parallel` output or behaviour; touching History / Benchmarks / Agents views;
plan editing in the TUI; new dependencies.

## Shared package: `internal/parallel`

Pure, UI-free pieces lifted out of `do.go` (which keeps its exact prompts and output):

- `DefaultJobs` (4) — the `--jobs` default, also the dashboard's concurrency bound.
- `StepID`, `StepTask`, `StepAgent`, `HealthyAgentNames` — step naming, prompt, per-step agent choice.
- `Graph` — scheduler nodes built from a plan (validated), with `done`/`dead` bookkeeping:
  `Ready()`, `MarkDone`, `MarkDead`, `Blocked()`, `Merged()`, and `WavesLeft()` (how many more waves
  if everything still pending succeeds — used for the "wave i/N" header).
- `BaseGuard` — the stray-write guard (`Arm` before a wave, `Check` after: discards anything written
  into the base tree/folder, reports whether it did). The CLI prints the same warning it did before.
- `RunStep` — the quiet per-task pipeline for UIs: `engine.Produce` in the isolated tree, then commit
  the result on the task branch when the tree is a git worktree (so `Isolator.Merge` can merge it).
  `do --parallel` keeps using `engine.ExecuteHeadless` (it prints).

## Dashboard flow

Entry: a Chat message `/parallel <request>` (alias `/par`). The single-run path is unchanged.

1. **Plan** (worker goroutine): in a git repo the tree must be clean (same rule as single runs); the
   default agent plans with `planner.MakeParallel`; the plan is validated and `worktree.New(dir)` picks
   git worktrees or folder copies. The plan is shown in the transcript and runs immediately — nothing is
   kept without review anyway.
2. **Wave** (worker goroutine): the guard is armed, a tree is added per ready step (sequentially — git
   worktree creation is not concurrency-safe), then `scheduler.Bounded(DefaultJobs)` runs `RunStep` per
   task. Each task's `OnEvent`/`Output` is tagged with its step id and forwarded over the wave's channel
   (`parEventMsg`, `parOutputMsg`, `parTaskDoneMsg`); after fan-in the guard check runs and
   `parWaveDoneMsg` closes the wave. Only `Update` mutates model state.
3. **Review** (model): failed tasks are reported with their error and skipped (dead); no-change tasks
   count as done; each task with changes opens the existing reviewer on `Isolator.Diff`. `y` →
   `Isolator.Merge` (a conflict is reported and counts as rejected), `n` → discard. Every tree is
   `Remove`d after its decision. The reviewer summary shows `wave i/N · task k/M`.
4. **Next wave** starts when all tasks are decided; when nothing is ready, blocked steps are listed, the
   isolator is cleaned up and a summary line closes the run.

Every task outcome is recorded to memory like single runs (`accepted`, `rejected`, `no-change`,
`failed`, `cancelled`), with the task's step prompt and diff, so History and history-aware routing see it.

## Live view

The run panel (side panel on wide terminals, stacked panel on narrow ones) becomes a task list for the
current wave. Each task is two lines: marker, step id, title, and right-aligned `status · agent ·
attempt n/N · elapsed`; below it, the last line of the task's output. Status is one of queued, running,
validating, retrying, done, failed (then merged / rejected / conflict / no changes / cancelled after
review). When the row is too narrow the agent and attempt are dropped before the title is truncated.

Keys while a wave runs: `↑/↓` or `j/k` select, `enter`/`tab` expand the selected task's full streamed
output into a full-body viewport (`↑↓ pgup pgdn` scroll), `esc` collapses; `esc` again cancels the run.
`shift+tab` still switches tabs. The status bar shows `wave i/N`.

## Cancellation

`esc` (or `ctrl+c`, which then quits) cancels the wave context; the worker waits for every task to stop,
runs the guard check (restoring stray writes into the base), and reports. The model then cleans up the
isolator (all temp trees), records running/queued tasks as cancelled, and — for `ctrl+c` — quits.
`ctrl+c` during a parallel review cleans up the isolator before quitting; already merged tasks stay
(they were explicitly accepted).

## Tests (headless, fake shell agents)

- task list rendering at several widths (fits, truncates, shows status/attempt/last line);
- event/output routing by task id updates only that row;
- select / expand / collapse keys;
- end to end in a plain temp dir and in a temp git repo: plan → wave 1 → review (accept both) → wave 2;
- conflict: two tasks editing the same file → second is reported as a conflict and not merged;
- ctrl+c mid-wave: no `orchestra-*` temp dirs remain (TMPDIR pointed at a test dir), base untouched;
- `internal/parallel`: graph waves, guard in both modes, `RunStep` commits in a worktree.
