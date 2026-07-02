# Orchestra — Build Plan

> **The operating system for AI coding agents.**
> One interface. Many coding agents. Run Claude Code, Codex, Gemini CLI, OpenCode and future
> agents through a single supervised orchestrator, and pick the best agent for every task.

## Guiding principles

1. **Ship a runnable vertical slice before building a platform.** Every milestone below must end
   in something a human can actually run and feel. No subsystem gets built until a real task needs it.
2. **Supervised first.** The human approves each agent's diff before it touches the repo. One task at
   a time. Autonomous, parallel, multi-agent-on-one-repo comes *much* later, only after the supervised
   loop is trusted.
3. **Abstract on the second implementation, not the first.** Don't write the `Agent` interface until a
   second agent forces its shape. Premature interfaces are dead weight.
4. **Never trust an LLM — verify.** The validation loop (build → test → show diff) is the product, not
   a late-stage nicety. It lands early.

---

## The interface (end-state vs. build order)

You will **not** open a terminal per agent. The end-state is a single persistent interface —
Claude Code / opencode style — where you chat with Orchestra and it dispatches to the best agent
under the hood, all in one session:

```
┌─ interactive shell (the real product — one session, you never leave it) ─┐
│  you type a message                                                      │
│        │                                                                 │
│        ▼                                                                 │
│   AI router ── picks the best agent (or answers directly) ──┐            │
│        │                                                    │            │
│        ▼                                                    ▼            │
│   runner → validate → review (accept/reject)  ◄── same engine as ───────┐│
└──────────────────────────────────────────────────────────────  `orchestra run`
```

The one-shot `orchestra run "..."` command built in M0 **is that engine**. The chat shell is a thin
loop wrapped around it, added in M1. We build the engine first because it's fully testable without a
UI — you don't want to debug the runner and a TUI at the same time. The `run` command survives as the
scriptable entrypoint (CI, workflows); the shell is what you'll live in day to day. **Same engine, two
front doors.**

**Routing has three levels; build them in order:**
1. **Static map** (config: task-kind → agent) — trivial fallback, always present.
2. **AI router** ⭐ — a cheap, fast LLM call reads your raw message, classifies intent
   (architecture / implementation / review / just-a-question), and picks the agent or answers directly.
   This is the layer that makes the chat interface feel smart, and it's the natural fit — a quick "fix
   this bug" has no planner step, so an LLM classification call is the right mechanism. **This is the
   `AI router layer` you asked for.**
3. **Learned / benchmarked** — data-driven routing from real benchmark results. Defer until benchmark
   mode (M6) exists to feed it.

---

## Milestone 0 — Walking skeleton (the whole idea in one command) ✅ DONE

**Goal:** this actually works, end to end, with a single hardcoded agent:

```
orchestra run "add a /health endpoint"
  → executes the coding CLI (e.g. `claude`) in the repo
  → streams stdout/stderr live
  → captures exit code
  → runs the project's tests
  → shows the resulting git diff
  → asks the human: accept / reject
  → on reject: git restores the working tree
```

If this feels good to use, the premise is validated. If it doesn't, nothing else matters yet.

### Tasks
- Cobra CLI with a single `run` command (no config system yet — flags are fine).
- **Runner:** exec the agent CLI, stream output, capture stdout/stderr/exit code, honor a timeout,
  support cancellation (Ctrl-C), set working directory.
- **Validation (minimal):** run a configurable test command, report pass/fail.
- **Supervised review:** show `git diff`, prompt accept/reject, `git checkout`/`git stash` on reject.
- Structured logging to stdout.

**Deliberately NOT in this milestone:** plugin system, agent interface, router, scheduler, TUI,
memory, benchmarks, context engine, workspaces, config files.

---

## Milestone 1 — Second agent → the abstraction earns its keep ✅ DONE

**Shipped:** `Agent` interface + `CLIAgent` + registry; OpenCode as the second agent; YAML config with
built-in defaults; interactive chat shell (default command) with `@agent` routing and `/agent` switch;
`orchestra agents` / `init`; Cobra CLI; shared `engine` package driving both `run` and the shell.
Key decision: agents run headless in **auto-approve mode** — Orchestra's diff review is the human gate.

**Goal:** `orchestra run "..." --agent codex` works, same supervised loop.

Adding the second agent is what reveals the *real* shape of the interface. Extract it now, not before.

```go
type Agent interface {
    Name() string
    Run(ctx context.Context, task Task) (Result, error)
    Health() error            // is the CLI installed / authed?
    Capabilities() []Capability
}
```

### Tasks
- Extract the `Agent` interface from the two concrete implementations (Claude, Codex).
- **Interactive shell:** running `orchestra` with no args drops you into a persistent chat session
  (the real product UX). Each message runs through the same engine as `run`. Manual agent selection
  for now — `@claude` / `@codex` prefix, or a default — since the AI router doesn't exist yet.
- `orchestra agents` — list installed/available agents + health status.
- Config file (YAML) so agent commands, test commands, and defaults aren't hardcoded flags.
- `orchestra init` — scaffold a config in a repo.

---

## Milestone 2 — Trustworthy validation loop ✅ DONE

**Shipped:** validation pipeline (`build → lint → test`, each configurable/skippable, stop-on-first-
failure); **self-correction loop** — a failing stage is fed back to the agent, which retries in place
up to `max_retries` times (default 2) before the human sees anything; per-stage report at the review
prompt; exit code reflects whether accepted changes actually pass. Config gains a `validate:` block and
`max_retries`; `run` gains `--retries`. Verified: self-correct→pass, retry exhaustion, accept-failing
→ non-zero exit, stop-on-first-failure ordering.

**Follow-up ✅ (from dogfooding):** validators are now **auto-detected** by ecosystem (Go / Node / Rust /
Python / plain JS) when none are configured, so the verify loop works out of the box for any project —
not just ones with a hand-written `validate:` block. Only checks whose toolchain is installed run. This
closed a real gap the dino-game test exposed (a JS project had `validation skipped`). Also fixed the same
round: the agent subprocess inherited stdin and drained the accept/reject input → silent auto-reject.

**Goal:** the diff proves itself before the human even looks at it. This is Orchestra's real edge.

### Tasks
- Validation pipeline: **build → fmt/lint → test**, each step configurable and skippable.
- **Feed failures back to the agent** for a bounded number of retries (agent gets its own test
  output and tries again). This closes the loop and is the single most valuable feature.
- Clear pass/fail summary in the review step, so accept/reject is an informed decision.

---

## Milestone 3 — Workflows (sequential, supervised) ✅ DONE

**Shipped:** `orchestra plan "<request>"` decomposes a request into ordered steps via a plan-capable
agent in query mode (stdout captured + JSON parsed leniently); `orchestra do "<request>"` plans →
human approves the plan → executes each step through the supervised engine sequentially, committing
each accepted step and halting on the first rejection; SQLite **memory** (`~/.orchestra/orchestra.db`,
outside the tree so it never dirties it) records every run; `orchestra history` shows recent runs +
the preferred agent (most-accepted for the project). Added `runner.RunCapture` + `agent.Querier`.
Also: **mimo** added to the built-in agents. Verified: plan parse, full workflow, halt-on-reject,
plan-decline, history + preferred-agent.



**Goal:** chain steps into a repeatable pipeline — still one task at a time, still human-approved.

```yaml
name: feature
steps:
  - plan       # decompose the request into tasks (no coding)
  - implement  # run the chosen agent
  - test       # validation loop
  - review     # human accept/reject
```

### Tasks
- **Planner:** turn a request ("build authentication") into an ordered task list. Decomposition only,
  no coding. Output is reviewable by the human before execution.
- **Workflow engine:** run steps sequentially, stop on failure, resume support.
- Memory (SQLite): store execution traces, last successful prompts, and per-project preferred agent.
  Start here — it's cheap and immediately useful for retries and history.

---

## Milestone 4 — AI router (Orchestra picks the agent itself) ✅ DONE

**Shipped:** `internal/router` with a pluggable `Classifier` interface (CLI classifier now — an agent
in query mode returning JSON; direct-API classifier can drop in later) + `Decision`; three-tier
resolution (AI suggestion → static `routes` map → default), choosing only healthy agents, that never
blocks. The shell is router-driven by default: plain questions are **answered inline** (no dispatch),
tasks auto-route with a printed reason; `@agent` still forces a choice and `/route on|off` toggles.
`do` honors planner-assigned per-step agents. Config gains a `router:` block. Verified with fakes
(question/implement/review/override/manual/garbage-fallback) and a **real claude smoke test**
(question classified + answered inline, tree untouched).

**Goal:** stop making the human pick the agent. You just type your message; Orchestra reads it and
dispatches to the best agent — the layer that makes the chat interface feel intelligent.

Now that M1–M3 exist (multiple agents, a shell, a planner), the router has something to choose between
and a place to plug in. Build the two cheap levels; defer the expensive one:

### Tasks
- **Static routing table** (config: task kind → preferred agent) — the deterministic fallback, and what
  the AI router falls back to when unsure.
- **AI router:** a small, fast LLM call classifies each incoming message (architecture / implementation /
  review / plain question) and picks the agent — or answers directly without dispatching for simple Q&A.
  This replaces the manual `@claude` / `@codex` selection from M1; the prefix stays as a manual override.
- Router explains its choice ("routing to Codex — implementation task") so the human can trust/override it.
- **Defer** data-driven scoring (speed / token / test-pass rate) until benchmark mode (M6) can feed it.

---

## Milestone 5 — Parallel & isolation (the genuinely hard milestone) ✅ DONE

**Shipped:** `internal/worktree` (one git worktree + branch per task, merge with conflict detection,
temp-dir root so the base tree stays clean); `internal/scheduler` (bounded-concurrency runner + DAG
cycle/unknown-dep detection + ready/blocked computation, unit-tested); engine `ExecuteHeadless`
(non-interactive run→validate→self-correct→commit-to-branch, sharing the M2 loop); planner emits
`depends_on`. `orchestra do --parallel [--jobs N]` runs **dependency waves**: every ready step runs
concurrently in its own worktree, then the human reviews + merges each result before the next wave
unlocks. Supervised-first preserved — the human gate moved to **merge time**. Verified: parallel
fan-out/fan-in, reject→partial, dependency-chain waves, plus unit tests for merge/conflict/scheduler.

**Goal:** run independent tasks concurrently without agents stepping on each other.

⚠️ This is where most orchestrators get messy — two agents editing one repo and auto-merging is deep.
Budget real fear here. Do not attempt before the supervised single-task loop is rock solid.

### Tasks
- **Workspace isolation via git worktrees** — one worktree per concurrent agent.
- **Scheduler:** dependency graph, priority queue, bounded concurrency.
- **Git integration:** branch per task, commit, diff, merge, conflict detection, rollback.
- Human still approves merges back to the main branch.

---

## Milestone 6 — Polish & differentiation

The nice-to-haves that make the project stand out — build only once the core is trusted and used.

- **CLI polish (styled REPL) ✅ DONE (ahead of schedule):** `internal/ui` — gradient ORCHESTRA banner
  with reveal animation, TTY-gated spinners during routing/planning/answering, colored diffs and status
  lines. Auto-disables color/animation when piped or `NO_COLOR` is set (regression stays green). The
  full-screen dashboard below is still the later, larger effort.
- **TUI dashboard (Bubble Tea) 🟡 PARTIAL:** `orchestra dashboard` — a full-screen read-only monitor
  with tabbed panels (Agents + live health probe, run History, Benchmark results), keyboard nav, styled
  with the gradient palette. Reads config + SQLite memory; doesn't launch work. **Follow-up:** live
  in-run monitoring (waves/steps/logs/timeline streaming) needs the engine to emit events instead of
  printing — a clean next step that would also fix interleaved output during `do --parallel`/`benchmark`.
- **Benchmark mode ✅ DONE:** `orchestra benchmark "<task>"` runs the task through every available
  agent, each in an isolated git worktree (parallel), then ranks a leaderboard by validation → retries
  → speed → diff size, offers to merge the winner, and records results to SQLite (`benchmarks` table)
  for future data-driven routing. Tokens/cost deferred (agent CLIs don't report usage generically).
- **Context engine:** git diff + changed files + ranking + token estimation, so you don't send the
  whole repo every time. Add when context size actually becomes a problem, not before.
- **Plugin SDK:** let others add agents (deepseek, aider, …) without touching core.

---

## Open question to settle before Milestone 5

The trust/UX model is already decided for M0–M4: **supervised, one task at a time, human approves every
diff.** Before parallel execution (M5), revisit: how autonomous do concurrent tasks get, and where does
the human stay in the loop (per-diff? per-merge? end-of-run only)? Answer that before writing the scheduler.

---

## Repository structure

Grow this as milestones need it — don't scaffold empty packages up front.

```text
orchestra/
├── cmd/orchestra/        # main + Cobra commands
├── internal/
│   ├── runner/           # M0: exec, stream, capture, cancel
│   ├── review/           # M0: diff + accept/reject
│   ├── validate/         # M0/M2: build, lint, test, retry loop
│   ├── agent/            # M1: Agent interface + implementations
│   ├── config/           # M1: YAML loader
│   ├── planner/          # M3
│   ├── workflow/         # M3
│   ├── memory/           # M3: SQLite
│   ├── router/           # M4
│   ├── scheduler/        # M5
│   ├── workspace/        # M5: git worktrees
│   ├── git/              # M5
│   ├── benchmark/        # M6
│   ├── context/          # M6
│   └── tui/              # M6
├── plugins/              # M6
├── configs/
├── examples/
├── docs/
└── scripts/
```

---

## Why this order

The original plan was a finished-product spec (16 phases, ~14 subsystems) rather than a build order —
that's a recipe for burning out on infrastructure before validating the idea. This version front-loads
the parts a user can *feel* (run an agent → verify → approve), defers everything speculative (benchmark
leaderboards, plugin SDK, learned routing) until real usage proves it's needed, and treats the two
genuinely hard problems honestly: **the validation/retry loop** (landed early, it's the differentiator)
and **parallel multi-agent isolation** (landed last, it's the danger zone).

Name ideas (pick before M0): Conductor, Maestro, Hive, Forge, Atlas, **Orchestra**, Nexus, Relay, Commander.
