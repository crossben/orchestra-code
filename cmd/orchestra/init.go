package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// starterConfig is written by `orchestra init`. It mirrors the built-in defaults
// so users can see and tweak them.
const starterConfig = `# Orchestra configuration.
# Agents run headless in auto-approve mode — Orchestra's diff review is the human
# gate, so each agent skips its own permission prompts.

default_agent: claude
timeout: 10m

# Lean-code principles injected into every task so agents write simpler, smaller
# changes (reuse first, no needless deps/abstractions). off | lite | full.
principles: lite

# Validation pipeline — each result must pass these before you review it.
# On failure, the agent is re-run with the failure output up to max_retries times.
# Leave the stages empty to auto-detect checks from the project (Go, Node, Rust,
# Python, or plain JS). Set explicit commands to override; set auto: false to
# disable detection entirely.
validate:
  build: ""             # e.g. "go build ./..."
  lint: ""              # e.g. "go vet ./..."
  test: ""              # e.g. "go test ./..."
  auto: true            # auto-detect validators when the above are empty
max_retries: 2

# AI router — reads each message and picks the agent (or answers questions directly).
router:
  enabled: true
  agent: claude         # who classifies / answers
  routes:               # static fallback: intent → agent
    plan: claude
    implement: opencode
    review: claude

agents:
  - name: claude
    bin: claude
    args: ["-p", "--dangerously-skip-permissions"]
    capabilities: [plan, implement, review]

  - name: opencode
    bin: opencode
    args: ["run", "--dangerously-skip-permissions"]
    dir_flag: "--dir"   # opencode ignores process cwd — needed for parallel worktree isolation
    capabilities: [implement, review]

  - name: mimo
    bin: mimo
    args: ["run", "--dangerously-skip-permissions"]
    capabilities: [implement, review]
`

func newInitCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starter orchestra.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path := filepath.Join(flagDir, "orchestra.yaml")
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists (use --force to overwrite)", path)
			} else if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.WriteFile(path, []byte(starterConfig), 0o644); err != nil {
				return err
			}
			fmt.Printf("wrote %s\n", path)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing config")
	return cmd
}
