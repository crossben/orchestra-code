package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/spf13/cobra"
)

func newRunCmd() *cobra.Command {
	var (
		agentName  string
		testCmd    string
		timeout    time.Duration
		retries    int
		principles string
		force      bool
	)

	cmd := &cobra.Command{
		Use:   `run "<task>"`,
		Short: "Dispatch one task to an agent (one-shot, scriptable)",
		Long: "Dispatch a single task to a coding agent, verify it, and review the diff.\n" +
			"This is the scriptable one-shot form; run `orchestra` with no args for the interactive shell.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.TrimSpace(strings.Join(args, " "))
			if task == "" {
				return errors.New(`a task description is required, e.g. orchestra run "add a health endpoint"`)
			}

			cfg, err := config.Load(flagDir)
			if err != nil {
				return err
			}
			// Flags override config.
			if agentName == "" {
				agentName = cfg.DefaultAgent
			}
			if timeout == 0 {
				timeout = cfg.TimeoutDuration()
			}
			// --test overrides the config's test stage; other stages still apply.
			if testCmd != "" {
				cfg.Validate.Test = testCmd
			}
			stages := stagesFor(cfg)
			maxRetries := cfg.RetryLimit()
			if cmd.Flags().Changed("retries") {
				maxRetries = retries
			}
			if cmd.Flags().Changed("principles") {
				cfg.Principles = principles
			}

			// Git pre-flight: the supervised loop reverts on reject.
			if !gitutil.IsRepo(flagDir) {
				return errNotRepo(flagDir)
			}
			clean, err := gitutil.IsClean(flagDir)
			if err != nil {
				return err
			}
			if !clean && !force {
				return errDirty()
			}

			reg := cfg.BuildRegistry()
			ag, ok := reg.Get(agentName)
			if !ok {
				return fmt.Errorf("unknown agent %q (see `orchestra agents`)", agentName)
			}
			if err := ag.Health(); err != nil {
				return fmt.Errorf("agent %q is not available: %w", agentName, err)
			}

			mem, err := openMemory()
			if err != nil {
				fmt.Printf("(warning: memory unavailable: %v)\n", err)
			}
			if mem != nil {
				defer mem.Close()
			}

			fmt.Printf("▸ task: %q\n", task)
			out, err := engine.Execute(cmd.Context(), bufio.NewReader(os.Stdin), engine.Options{
				Agent:          ag,
				Prompt:         task,
				Dir:            flagDir,
				Stages:         stages,
				MaxRetries:     maxRetries,
				Timeout:        timeout,
				CommitOnAccept: false, // one-shot: leave changes for the user to commit
				Memory:         mem,
				Principles:     config.PrinciplesText(cfg.Principles),
			})
			if err != nil {
				return err
			}
			// Non-zero exit only when accepted changes still fail validation.
			if out.Accepted && !out.Report.Skipped && !out.Report.Passed() {
				return errors.New("accepted changes did not pass validation")
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "agent to dispatch (default from config)")
	cmd.Flags().StringVar(&testCmd, "test", "", `test command (overrides the config's test stage), e.g. "go test ./..."`)
	cmd.Flags().DurationVar(&timeout, "timeout", 0, "max time for the agent to run (default from config)")
	cmd.Flags().IntVar(&retries, "retries", 0, "self-correction retries on validation failure (default from config)")
	cmd.Flags().StringVar(&principles, "principles", "", "lean-code principles preamble: off|lite|full (default from config)")
	cmd.Flags().BoolVar(&force, "force", false, "allow running with a dirty working tree")
	return cmd
}
