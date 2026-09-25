# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

The canonical agent instructions live in AGENTS.md (shared with other coding agents). It covers the
build/verify commands, package layout, the supervised loop, the Agent interface, and testing conventions:

@AGENTS.md

## Additions for Claude Code

- Module path is `github.com/crossben/orchestra-code`; CI pins Go 1.22. If `go` tries to download a
  newer toolchain and fails offline, run `go env -w GOTOOLCHAIN=local` once.
- Run a single test: `go test ./internal/engine/ -run TestName -v`.
- CI also runs `goreleaser check` against `.goreleaser.yaml`, so keep that file valid if you touch it.
  See docs/RELEASING.md for the release flow and docs/EXTENDING.md for adding agents (CLI or `type: api`).
- Orchestra is supervised-first: nothing an agent produces is kept without an explicit accept at diff review.
  If a change alters agent behavior or the supervised loop, call that out explicitly in the PR.
