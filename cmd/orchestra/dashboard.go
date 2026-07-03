package main

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/router"
	"github.com/crossben/orchestra-code/internal/tui"
	"github.com/spf13/cobra"
)

func newDashboardCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "dashboard",
		Aliases: []string{"dash"},
		Short:   "Full-screen dashboard: agents (live probe), history, benchmarks, and chat",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(flagDir)
			if err != nil {
				return err
			}
			reg := cfg.BuildRegistry()

			mem, err := openMemory()
			if err == nil {
				defer mem.Close()
			} else {
				mem = nil
			}

			dir, err := absDir()
			if err != nil {
				return err
			}

			// Build the AI router (best-effort) so the Chat tab can auto-route.
			var rtr *router.Router
			routingOn := cfg.RouterEnabled()
			if routingOn {
				if r, rerr := cfg.BuildRouter(reg); rerr != nil {
					fmt.Printf("(note: AI routing off in chat: %v)\n", rerr)
					routingOn = false
				} else {
					rtr = r
				}
			}

			stages, _ := cfg.ResolveStages(flagDir) // resolve silently (no "auto-detected" print in the TUI)
			model := tui.New(tui.Deps{
				Ctx:          cmd.Context(),
				Cfg:          cfg,
				Reg:          reg,
				Mem:          mem,
				Dir:          dir,
				Router:       rtr,
				RoutingOn:    routingOn,
				Stages:       stages,
				MaxRetries:   cfg.RetryLimit(),
				Timeout:      cfg.TimeoutDuration(),
				Principles:   config.PrinciplesText(cfg.Principles),
				DefaultAgent: cfg.DefaultAgent,
			})
			p := tea.NewProgram(model, tea.WithAltScreen(), tea.WithContext(cmd.Context()))
			_, err = p.Run()
			return err
		},
	}
}
