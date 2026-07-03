package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newHistoryCmd() *cobra.Command {
	var (
		all   bool
		limit int
	)
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show recent runs and the preferred agent for this project",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			mem, err := openMemory()
			if err != nil {
				return err
			}
			defer mem.Close()

			dir, err := absDir()
			if err != nil {
				return err
			}
			key := dir
			if all {
				key = "" // all projects
			}

			runs, err := mem.Recent(key, limit)
			if err != nil {
				return err
			}
			if len(runs) == 0 {
				fmt.Println("no history yet")
				return nil
			}

			if !all {
				if pref, n, err := mem.PreferredAgent(dir); err == nil && pref != "" {
					fmt.Printf("preferred agent here: %s (%d accepted)\n\n", pref, n)
				}
			}

			fmt.Printf("%-19s %-10s %-10s %-4s %s\n", "WHEN", "AGENT", "OUTCOME", "ATT", "TASK")
			for _, r := range runs {
				task := firstLine(r.Prompt)
				if len(task) > 48 {
					task = task[:47] + "…"
				}
				fmt.Printf("%-19s %-10s %-10s %-4d %s\n",
					r.Time.Local().Format("2006-01-02 15:04:05"), r.Agent, r.Outcome, r.Attempts, task)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "show runs across all projects")
	cmd.Flags().IntVar(&limit, "limit", 20, "max rows to show")
	return cmd
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
