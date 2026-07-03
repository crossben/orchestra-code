#!/usr/bin/env bash
# notes-agent.sh — a minimal example "agent" for Orchestra.
#
# It demonstrates the agent contract: Orchestra invokes an agent as
#     <bin> [configured args...] "<task prompt>"
# with the working directory set to the target repo (its own git worktree during
# parallel/benchmark runs). The agent edits files in that directory; Orchestra
# captures the resulting git diff for the supervised review.
#
# This example just records the task into NOTES.md — enough to see the full
# Orchestra loop (dispatch → diff → accept/reject) with no external dependencies.
set -euo pipefail

# The task prompt is the last argument. Orchestra prepends brief guidance to
# every prompt (proceed-on-assumptions + lean-code principles), which a real
# agent reads as instructions; the user's actual task is the last line.
prompt="${!#}"
task="$(printf '%s' "$prompt" | tail -n1)"

printf -- '- %s\n' "$task" >> NOTES.md
echo "notes-agent: appended the task to NOTES.md"
