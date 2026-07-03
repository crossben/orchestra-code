package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/scheduler"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/spf13/cobra"
)

func newAgentsCmd() *cobra.Command {
	var (
		probe        bool
		probeTimeout time.Duration
	)
	cmd := &cobra.Command{
		Use:   "agents",
		Short: "List configured agents and their availability",
		Long: "List agents and whether their binary is installed. With --probe, actually run a trivial\n" +
			"task against each installed agent to check it can really work (catches auth/billing errors\n" +
			"and hangs that an installed-binary check misses).",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(flagDir)
			if err != nil {
				return err
			}
			reg := cfg.BuildRegistry()

			if probe {
				return probeAgents(cmd, reg, cfg, probeTimeout)
			}

			fmt.Printf("%-12s %-16s %s\n", "AGENT", "STATUS", "CAPABILITIES")
			for _, a := range reg.All() {
				status := "available"
				if err := a.Health(); err != nil {
					status = "not installed"
				}
				def := ""
				if a.Name() == cfg.DefaultAgent {
					def = " (default)"
				}
				fmt.Printf("%-12s %-16s %s%s\n", a.Name(), status, capList(a), def)
			}
			fmt.Println(ui.Dim("\ntip: `orchestra agents --probe` checks agents can actually run, not just that they're installed"))
			return nil
		},
	}
	cmd.Flags().BoolVar(&probe, "probe", false, "run a live trivial task against each installed agent to verify it works")
	cmd.Flags().DurationVar(&probeTimeout, "probe-timeout", 45*time.Second, "per-agent probe timeout")
	return cmd
}

// probeAgents runs a live health probe against every installed agent concurrently.
func probeAgents(cmd *cobra.Command, reg *agent.Registry, cfg *config.Config, timeout time.Duration) error {
	all := reg.All()
	type row struct {
		name   string
		result string
		detail string
	}
	rows := make([]row, len(all))

	fmt.Printf("%s probing %d agents (up to %s each)…\n", ui.Accent("▸"), len(all), timeout)

	scheduler.Bounded(cmd.Context(), len(all), len(all), func(i int) error {
		a := all[i]
		rows[i].name = a.Name()
		if err := a.Health(); err != nil {
			rows[i].result = ui.Dim("not installed")
			return nil
		}
		p, ok := a.(agent.Prober)
		if !ok {
			rows[i].result = ui.Dim("n/a")
			rows[i].detail = "agent does not support probing"
			return nil
		}
		res := p.Probe(cmd.Context(), timeout)
		if res.OK {
			rows[i].result = ui.Success("✓ works")
		} else {
			rows[i].result = ui.Danger("✗ failing")
		}
		rows[i].detail = res.Detail
		return nil
	})

	fmt.Printf("\n%-12s %-14s %s\n", "AGENT", "PROBE", "DETAIL")
	for _, r := range rows {
		fmt.Printf("%-12s %-14s %s\n", ui.Agent(r.name), r.result, ui.Dim(r.detail))
	}
	return nil
}

func capList(a agent.Agent) string {
	caps := make([]string, 0, len(a.Capabilities()))
	for _, c := range a.Capabilities() {
		caps = append(caps, string(c))
	}
	return strings.Join(caps, ",")
}
