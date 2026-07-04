# Contributing to Orchestra

Thanks for your interest! Orchestra is **the operating system for AI coding agents** — one
supervised interface that runs Claude Code, OpenCode, Mimo, and others. Contributions of all
sizes are welcome.

## Development setup

Requirements: **Go 1.22+** and **git**.

```sh
git clone https://github.com/crossben/orchestra-code
cd orchestra-code
go build -o bin/orchestra ./cmd/orchestra
./bin/orchestra --help
```

Common tasks:

```sh
go build ./...      # build everything
go test ./...       # run the tests
go vet ./...        # static checks
gofmt -w .          # format (CI enforces this)
```

> If `go` tries to download a newer toolchain and fails offline, run once:
> `go env -w GOTOOLCHAIN=local` (the repo targets Go 1.22).

## Project layout

```
cmd/orchestra    CLI (cobra): run / do / plan / benchmark / dashboard / agents / init / shell
internal/agent   Agent interface + CLIAgent + registry            ← the extension point
internal/engine  supervised pipeline: dispatch → validate → retry → review
internal/router  AI routing (classify → decide)
internal/planner request → ordered steps (+ dependencies)
internal/scheduler bounded concurrency + dependency waves
internal/worktree  git-worktree isolation for parallel work
internal/validate  build → lint → test pipeline (+ auto-detection)
internal/memory  SQLite history + benchmarks
internal/tui     Bubble Tea dashboard (agents/history/benchmarks/chat)
internal/ui      terminal styling
```

See [docs/EXTENDING.md](docs/EXTENDING.md) to add an agent, and [docs/RELEASING.md](docs/RELEASING.md)
for how releases are cut.

## Testing conventions

- Unit tests are colocated (`*_test.go`). Pure logic (scheduler, config detection, TUI update/view)
  is tested headlessly — no real agent or TTY required.
- End-to-end behavior is exercised with **fake agents** (small shell scripts) so tests are fast,
  offline, and deterministic. Please follow this pattern rather than calling real agent CLIs in tests.
- Keep `go test ./...`, `go vet ./...`, and `gofmt -l .` clean — CI runs all three.

## Pull requests

1. Branch off `main`.
2. Keep changes focused; add/adjust tests.
3. Run `go build ./... && go vet ./... && go test ./... && gofmt -w .` before pushing.
4. Describe the change and how you verified it.

## Design principles

Orchestra is **supervised-first**: agents run headless in auto-approve mode, and the human gate is
the diff review — nothing is kept without an explicit accept. Prefer small, verifiable changes and
lean toward reusing existing packages over reinventing. If a change alters agent behavior or the
supervised loop, call that out explicitly in the PR.
