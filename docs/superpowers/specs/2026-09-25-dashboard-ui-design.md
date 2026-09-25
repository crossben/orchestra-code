# Dashboard UI overhaul — design

Date: 2026-09-25 · Branch: `feat/dashboard-ui` · Scope: `orchestra dashboard` (`internal/tui`), plus
small, optional hooks in `internal/engine`, `internal/agent`, `internal/runner`, `internal/validate`,
`internal/memory`, `internal/gitutil`.

## Intent

Make the dashboard the place people actually work in: you can **watch a run as it happens**,
**review changes like a small code-review tool**, it **looks polished**, and the known **rough edges are
gone**. Success = a dashboard session where a user sends a task, sees routing → agent output → each
validation stage live, then reviews a multi-file diff file-by-file and accepts/rejects — and can reopen
that diff later from History, even after restarting.

Non-goals: changing the plain shell (`internal/shell`) or `run`/`do` output; mouse support; new
third-party dependencies (Bubble Tea / Lipgloss / Glamour / Chroma already cover everything).

## Constraints

- The engine stays the single pipeline. New hooks are **optional** (nil = today's behaviour), so `run`,
  `do`, shell and benchmark are untouched.
- No new agent interface: streaming is an optional `io.Writer` on `agent.Task`.
- CGO-free, stdlib + existing deps only. Tests headless and offline with fake agents.

## Phases (each ships and passes CI on its own)

### Phase 1 — Foundation + rough edges

- **Split `tui.go`** (943 lines) into focused files: `tui.go` (model, messages, Update routing),
  `theme.go` (palette + styles), `chrome.go` (header, status bar), `chat.go`, `views.go`
  (agents/history/bench/logs), `changes.go`, `table.go` (width-aware columns).
- **Tab order**: Chat · Changes · History · Agents · Benchmarks · Logs. Dashboard opens on **Chat**.
- **Width-aware tables**: pad cells by rendered width (`lipgloss.Width`), so coloured cells no longer
  skew columns.
- **Record the routed agent** on changes (today it always records the default agent).
- **Dashboard runs are recorded to history** (accepted / rejected / no-change / failed); today the
  dashboard writes nothing to memory, so its runs never appear in History.
- **Persist diffs**: `memory.Run` gains `Diff`; the `runs` table gains `diff TEXT NOT NULL DEFAULT ''`
  via an idempotent `ALTER TABLE` migration. The engine's `recordMemory` stores the diff too.
- **Changes tab is persistent**: built from history runs that have a diff (falls back to the in-session
  list when no memory store is available).
- **History rows are selectable**; `enter` opens that run's diff in the review screen.
- Esc in Chat no longer jumps to Agents; `q` quits outside Chat, `ctrl+c` quits everywhere.

### Phase 2 — Review screen

One `reviewer` component used by Chat review, Changes and History:

```
 REVIEW  opencode · attempt 2/3 · build ✓  lint ✓  test ✓          3 files  +42 −7
┌ files ───────────────┐┌ internal/api/health.go ─────────────────────────── +30 −0 ┐
│▸ internal/api/hea… +30││ @@ -0,0 +1,30 @@                                          │
│  internal/api/rout… +4 ││ +package api                                              │
│  README.md      +8 −7 ││ …                                                         │
└──────────────────────┘└───────────────────────────────────────────────────────────┘
 y accept · n reject · [ ] file · ↑↓ scroll · a all files
```

- `parseDiff(unified) []fileDiff{Path, Added, Removed, Body, Binary, Status(new/deleted/modified)}` —
  counting `+`/`-` lines directly (drops the `go-diff` usage in `diffStats`).
- File list (left, ~28 cols, hidden under 90 cols wide) + scrollable per-file diff (right, `viewport`,
  chroma-highlighted). `[`/`]` (and `J`/`K`) move between files, `a` toggles all-files view, ↑↓ / pgup /
  pgdn / j / k / g / G scroll.
- Summary bar: agent, attempts, per-stage validation badges, file count, total `+/−`.
- Validation failure: the failing stage's output tail is shown above the diff so you can judge it.
- Read-only mode (Changes/History) hides accept/reject; `esc` goes back to the list.

### Phase 3 — Live runs

Engine (optional hooks, nil-safe):

```go
// engine
type EventKind int // EventAttempt, EventAgentDone, EventStageStart, EventStageDone, EventDiff
type Event struct {
    Kind            EventKind
    Attempt, Max    int
    Stage           string
    Passed          bool
    ExitCode        int
    Duration        time.Duration
}
type Options struct { …; OnEvent func(Event); Output io.Writer }
```

- `validate.RunPipelineObserved(ctx, dir, stages, func(name string, done bool, res StageResult))`;
  `RunPipeline` delegates with a nil observer.
- `agent.Task.Output io.Writer` — when set, `CLIAgent.RunQuiet` tees combined stdout/stderr into it
  (via new `runner.Spec.Output`); `APIAgent` writes one line when it sends the request and one with the
  outcome (it has no token stream).
- `engine.Produce` emits events and passes `Output` through. `Execute`/`ExecuteHeadless` unchanged.

TUI:

- `produceCmd` runs in a goroutine and forwards events / output lines over a channel; a
  `waitForRun(ch)` command re-arms itself until the final `turnMsg` (standard Bubble Tea pattern).
- **Run panel** — a stage timeline: `✓ routed → opencode (implementation)`, `● attempt 1/3 · 0:42`,
  `○ build ○ lint ○ test`, updating live; plus the tail of agent output (ring buffer, 400 lines).
  Beside the transcript when the terminal is ≥ 110 cols wide, stacked above the input otherwise.
- **Cancel**: `esc` while running cancels the run's context; any partial changes are reverted
  (`engine.Revert` with the turn's baseline) and the transcript says so.
- Elapsed timer in the status bar while running.

### Phase 4 — Visual design

- **Theme** (`theme.go`): one palette of `lipgloss.AdaptiveColor`s (violet accent, cyan secondary,
  green/red/amber semantic, muted gray) so light terminals are readable; every style derives from it.
- **Header**: `⬡ ORCHESTRA` wordmark, pill tabs with number hints, right-aligned context:
  repo folder name + git branch (or `plain folder`).
- **Status bar**: coloured mode pill (`READY` / `RUNNING` / `REVIEW`) · active agent / routing ·
  key hints · transient status / elapsed time, right-aligned.
- **Chat transcript**: message blocks with a coloured left border per role (you / agent / system),
  agent replies rendered as markdown.
- **Agents tab**: adds per-agent stats from history (runs, accept rate, last used).
- **Empty states**: centred, bordered hint boxes that say what to do next.

## Error handling

- Stream writer never blocks the agent: lines are sent non-blockingly to a buffered channel; if the UI
  falls behind, lines are dropped (the final full output still arrives with the turn).
- Migration failure is non-fatal the same way memory already is (dashboard runs without history).
- Any render helper (chroma, glamour) keeps its raw-text fallback.

## Testing

- `runner`: `Spec.Output` receives combined output.
- `validate`: observer sees start/done per stage, stops at first failure.
- `engine`: `Produce` with a fake agent emits attempt → agent done → stage events → retry, and streams output.
- `memory`: new `diff` column round-trips; migration is idempotent on an old-schema DB.
- `tui`: `parseDiff` counts/paths/status; reviewer file navigation and scrolling; tab order and
  default tab; width-aware padding with ANSI; run panel renders events; cancel returns to idle; history
  enter opens review; existing tests updated to the new tab order.
