package main

import (
	"fmt"

	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/router"
	"github.com/crossben/orchestra-code/internal/shell"
	"github.com/spf13/cobra"
)

// version is the build version. It defaults to the last released tag for
// `go install`, and is overridden at release time via
// -ldflags "-X main.version=<tag>" (see .goreleaser.yaml).
var version = "0.7.1"

// persistent flags shared across subcommands.
var (
	flagDir string
)

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "orchestra",
		Short:         "The operating system for AI coding agents",
		Long:          "Orchestra runs coding agents (Claude, OpenCode, …) through one supervised interface.",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		// No subcommand → start the interactive shell.
		RunE: func(cmd *cobra.Command, args []string) error {
			return runShell(cmd)
		},
	}

	root.PersistentFlags().StringVar(&flagDir, "dir", ".", "repository/working directory")

	root.AddCommand(newRunCmd())
	root.AddCommand(newAgentsCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newPlanCmd())
	root.AddCommand(newDoCmd())
	root.AddCommand(newHistoryCmd())
	root.AddCommand(newBenchmarkCmd())
	root.AddCommand(newDashboardCmd())
	return root
}

// runShell wires up config + registry and launches the interactive session.
func runShell(cmd *cobra.Command) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}
	if !gitutil.IsRepo(flagDir) {
		return errNotRepo(flagDir)
	}
	clean, err := gitutil.IsClean(flagDir)
	if err != nil {
		return err
	}
	if !clean {
		return errDirty()
	}

	mem, err := openMemory()
	if err != nil {
		fmt.Printf("(warning: memory unavailable: %v)\n", err)
	}
	if mem != nil {
		defer mem.Close()
	}

	reg := cfg.BuildRegistry()

	// Build the AI router (best-effort — the shell still works manually if the
	// router can't be constructed, e.g. the router agent isn't installed).
	var rtr *router.Router
	routingOn := cfg.RouterEnabled()
	if routingOn {
		r, rerr := cfg.BuildRouter(reg)
		if rerr != nil {
			fmt.Printf("(warning: AI routing disabled: %v)\n", rerr)
			routingOn = false
		} else {
			rtr = r
		}
	}

	sh := shell.New(reg, flagDir, cfg.DefaultAgent, stagesFor(cfg), cfg.RetryLimit(), cfg.TimeoutDuration(), mem, rtr, routingOn, config.PrinciplesText(cfg.Principles))
	return sh.Run(cmd.Context())
}
