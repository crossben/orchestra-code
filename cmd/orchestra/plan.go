package main

import (
	"errors"
	"fmt"
	"strings"

	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/planner"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/spf13/cobra"
)

func newPlanCmd() *cobra.Command {
	var agentName string
	cmd := &cobra.Command{
		Use:   `plan "<request>"`,
		Short: "Decompose a request into an ordered task list (no coding)",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request := strings.TrimSpace(strings.Join(args, " "))
			if request == "" {
				return errors.New(`a request is required, e.g. orchestra plan "build authentication"`)
			}
			cfg, err := config.Load(flagDir)
			if err != nil {
				return err
			}
			if agentName == "" {
				agentName = cfg.DefaultAgent
			}
			p, err := buildPlanner(cfg, agentName)
			if err != nil {
				return err
			}

			fmt.Printf("%s planning with %s\n", ui.Accent("▸"), ui.Agent(p.AgentName()))
			sp := ui.Spin("planning…")
			pl, err := p.Make(cmd.Context(), request, flagDir)
			sp.Stop()
			if err != nil {
				return err
			}
			printPlan(pl.Steps)
			return nil
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "planning agent (default from config)")
	return cmd
}

// buildPlanner resolves an agent and wraps it as a planner.
func buildPlanner(cfg *config.Config, agentName string) (*planner.Planner, error) {
	reg := cfg.BuildRegistry()
	ag, ok := reg.Get(agentName)
	if !ok {
		return nil, fmt.Errorf("unknown agent %q (see `orchestra agents`)", agentName)
	}
	if err := ag.Health(); err != nil {
		return nil, fmt.Errorf("agent %q is not available: %w", agentName, err)
	}
	return planner.New(ag, cfg.TimeoutDuration())
}

func printPlan(steps []planner.Step) {
	fmt.Printf("\n%s\n", ui.Heading(fmt.Sprintf("plan — %d steps", len(steps))))
	for i, s := range steps {
		dep := ""
		if len(s.DependsOn) > 0 {
			parts := make([]string, len(s.DependsOn))
			for j, d := range s.DependsOn {
				parts[j] = fmt.Sprintf("%d", d)
			}
			dep = ui.Dim(" (after " + strings.Join(parts, ",") + ")")
		}
		ag := ""
		if s.Agent != "" {
			ag = " " + ui.Agent("@"+s.Agent)
		}
		fmt.Printf("  %s %s%s%s\n", ui.Accent(fmt.Sprintf("%d.", i+1)), s.Title, ag, dep)
		if s.Detail != "" {
			fmt.Printf("     %s\n", ui.Dim(s.Detail))
		}
	}
}
