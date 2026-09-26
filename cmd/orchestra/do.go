package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/parallel"
	"github.com/crossben/orchestra-code/internal/planner"
	"github.com/crossben/orchestra-code/internal/review"
	"github.com/crossben/orchestra-code/internal/scheduler"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/validate"
	"github.com/crossben/orchestra-code/internal/worktree"
	"github.com/spf13/cobra"
)

func newDoCmd() *cobra.Command {
	var (
		agentName  string
		yes        bool
		inParallel bool
		jobs       int
		principles string
	)
	cmd := &cobra.Command{
		Use:   `do "<request>"`,
		Short: "Plan a request, then execute each step supervised (sequential or parallel)",
		Long: "Decompose a request into steps, let you approve the plan, then run each step through the\n" +
			"supervised engine. Sequential by default (one step at a time, halt on rejection). With\n" +
			"--parallel, independent steps run concurrently in isolation (git worktrees inside a repository,\n" +
			"directory copies in a plain folder) and you review + merge each result.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			request := strings.TrimSpace(strings.Join(args, " "))
			if request == "" {
				return errors.New(`a request is required, e.g. orchestra do "build authentication"`)
			}

			cfg, err := config.Load(flagDir)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("principles") {
				cfg.Principles = principles
			}
			if agentName == "" {
				agentName = cfg.DefaultAgent
			}

			// Git pre-flight: both modes work anywhere. Inside a repository the
			// tree must start clean; --parallel isolates steps in git worktrees
			// there, and in directory copies in a plain folder.
			inRepo := gitutil.IsRepo(flagDir)
			if inRepo {
				if clean, err := gitutil.IsClean(flagDir); err != nil {
					return err
				} else if !clean {
					return errDirty()
				}
			}

			reg := cfg.BuildRegistry()
			ag, ok := reg.Get(agentName)
			if !ok {
				return fmt.Errorf("unknown agent %q (see `orchestra agents`)", agentName)
			}
			if err := ag.Health(); err != nil {
				return fmt.Errorf("agent %q is not available: %w", agentName, err)
			}

			// 1. Plan (dependency-aware when running in parallel).
			p, err := buildPlanner(cfg, agentName)
			if err != nil {
				return err
			}
			fmt.Printf("%s planning with %s\n", ui.Accent("▸"), ui.Agent(p.AgentName()))
			sp := ui.Spin("planning…")
			var pl planner.Plan
			if inParallel {
				pl, err = p.MakeParallel(cmd.Context(), request, flagDir, parallel.HealthyAgentNames(reg))
			} else {
				pl, err = p.Make(cmd.Context(), request, flagDir)
			}
			sp.Stop()
			if err != nil {
				return err
			}
			printPlan(pl.Steps)

			in := bufio.NewReader(os.Stdin)
			if !yes && !confirm(in, "\nproceed with this plan?") {
				fmt.Println("aborted — no changes made")
				return nil
			}

			// Memory (best-effort).
			mem, err := openMemory()
			if err != nil {
				fmt.Printf("  (warning: memory unavailable: %v)\n", err)
			}
			if mem != nil {
				defer mem.Close()
			}

			stages := stagesFor(cfg)
			if inParallel {
				return runParallel(cmd.Context(), in, cfg, reg, ag, agentName, pl, stages, jobs, mem)
			}
			return runSequential(cmd.Context(), in, cfg, reg, ag, agentName, pl, stages, mem)
		},
	}
	cmd.Flags().StringVar(&agentName, "agent", "", "agent for planning and implementation (default from config)")
	cmd.Flags().BoolVar(&yes, "yes", false, "skip the plan-approval prompt")
	cmd.Flags().BoolVar(&inParallel, "parallel", false, "run independent steps concurrently in isolated worktrees (or folder copies)")
	cmd.Flags().IntVar(&jobs, "jobs", parallel.DefaultJobs, "max concurrent steps when --parallel")
	cmd.Flags().StringVar(&principles, "principles", "", "lean-code principles preamble: off|lite|full (default from config)")
	return cmd
}

// runSequential runs steps one at a time in the base tree, committing each
// accepted step and halting at the first rejection.
func runSequential(ctx context.Context, in *bufio.Reader, cfg *config.Config, reg *agent.Registry, ag agent.Agent, agentName string, pl planner.Plan, stages []validate.Stage, mem *memory.Store) error {
	retries := cfg.RetryLimit()
	for i, step := range pl.Steps {
		stepAgent := parallel.StepAgent(reg, step, ag, agentName)
		fmt.Printf("\n%s %s  %s\n",
			ui.Accent(fmt.Sprintf("═══ step %d/%d", i+1, len(pl.Steps))),
			ui.Heading(step.Title), ui.Dim("["+stepAgent.Name()+"]"))
		out, err := engine.Execute(ctx, in, engine.Options{
			Agent:          stepAgent,
			Prompt:         parallel.StepTask(step),
			Dir:            flagDir,
			Stages:         stages,
			MaxRetries:     retries,
			Timeout:        cfg.TimeoutDuration(),
			CommitOnAccept: true,
			Memory:         mem,
			Principles:     config.PrinciplesText(cfg.Principles),
		})
		if err != nil {
			return fmt.Errorf("step %d (%s): %w", i+1, step.Title, err)
		}
		if !out.Accepted {
			fmt.Printf("\n%s\n", ui.Warn(fmt.Sprintf("■ workflow halted at step %d/%d (step not accepted). "+
				"Prior accepted steps are committed.", i+1, len(pl.Steps))))
			return nil
		}
	}
	kept := "accepted and committed"
	if !gitutil.IsRepo(flagDir) {
		kept = "accepted — changes left in the folder" // git-free: nothing is committed
	}
	fmt.Printf("\n%s\n", ui.Success(fmt.Sprintf("✓ workflow complete — all %d steps %s", len(pl.Steps), kept)))
	return nil
}

// runParallel executes the plan in dependency waves: every step whose deps are
// merged runs concurrently in its own isolated tree (a git worktree, or a
// directory copy outside a repository), then results are reviewed and merged
// one at a time before the next wave unlocks.
func runParallel(ctx context.Context, in *bufio.Reader, cfg *config.Config, reg *agent.Registry, ag agent.Agent, agentName string, pl planner.Plan, stages []validate.Stage, jobs int, mem *memory.Store) error {
	mgr, err := worktree.New(flagDir)
	if err != nil {
		return err
	}
	defer mgr.Cleanup()
	guard := parallel.NewBaseGuard(flagDir)

	g, err := parallel.NewGraph(pl)
	if err != nil {
		return err
	}

	retries := cfg.RetryLimit()
	id := parallel.StepID

	type result struct {
		tree    worktree.Tree
		out     engine.Outcome
		err     error
		created bool
	}

	for wave := 1; ; wave++ {
		ready := g.Ready()
		if len(ready) == 0 {
			break
		}
		fmt.Printf("\n%s\n", ui.Heading(fmt.Sprintf("── wave %d: %d step(s) in parallel ──", wave, len(ready))))
		if err := guard.Arm(); err != nil {
			return err
		}

		// Fan-out: run the ready steps concurrently, each in its own worktree.
		results := make([]result, len(ready))
		scheduler.Bounded(ctx, jobs, len(ready), func(k int) error {
			i := ready[k]
			tree, aerr := mgr.Add(id(i), "HEAD")
			if aerr != nil {
				results[k] = result{err: aerr}
				return aerr
			}
			out, rerr := engine.ExecuteHeadless(ctx, engine.Options{
				Agent:      parallel.StepAgent(reg, pl.Steps[i], ag, agentName),
				Prompt:     parallel.StepTask(pl.Steps[i]),
				Dir:        tree.Dir,
				Stages:     stages,
				MaxRetries: retries,
				Timeout:    cfg.TimeoutDuration(),
				Memory:     mem,
				Label:      "step " + id(i),
				Principles: config.PrinciplesText(cfg.Principles),
			})
			results[k] = result{tree: tree, out: out, err: rerr, created: true}
			return rerr
		})

		// Defend the merge: if an agent wrote into the base tree instead of its
		// worktree (some CLIs don't honor the working directory), the base is now
		// dirty and merges would fail. Discard that stray work so the properly
		// isolated branches can still merge cleanly.
		if guard.Check() {
			if guard.InRepo() {
				fmt.Println(ui.Warn("  ! an agent wrote outside its worktree — discarding stray changes in the base tree"))
			} else {
				fmt.Println(ui.Warn("  ! an agent wrote outside its isolated copy — discarding stray changes in the base folder"))
			}
		}

		// Fan-in: review + merge each result in order.
		for k, i := range ready {
			r := results[k]
			step := pl.Steps[i]
			fmt.Printf("\n%s %s\n", ui.Accent(fmt.Sprintf("review step %d/%d", i+1, len(pl.Steps))), ui.Heading(step.Title))

			if r.err != nil {
				fmt.Printf("  %s %v\n", ui.Danger("failed:"), r.err)
				g.MarkDead(i)
				if r.created {
					mgr.Remove(r.tree)
				}
				continue
			}
			if r.out.ExitCode != 0 {
				fmt.Printf("  %s agent exited abnormally (code %d) — skipping this step\n",
					ui.Danger("✗"), r.out.ExitCode)
				g.MarkDead(i)
				mgr.Remove(r.tree)
				continue
			}
			if !r.out.HadChanges {
				fmt.Println(ui.Dim("  no changes produced — nothing to merge"))
				g.MarkDone(i)
				mgr.Remove(r.tree)
				continue
			}

			diff, derr := mgr.Diff(r.tree)
			if derr != nil {
				mgr.Remove(r.tree)
				return derr
			}
			if review.Prompt(in, diff, r.out.Report) {
				conflict, merr := mgr.Merge(r.tree, fmt.Sprintf("Merge step %d: %s", i+1, step.Title))
				if merr != nil {
					mgr.Remove(r.tree)
					return merr
				}
				if conflict {
					fmt.Println(ui.Danger("  ✗ merge conflict — left unmerged; dependent steps will be skipped"))
					g.MarkDead(i)
				} else {
					fmt.Println(ui.Success("  ✓ merged into base"))
					g.MarkDone(i)
				}
			} else {
				fmt.Println(ui.Warn("  ↺ rejected — discarded"))
				g.MarkDead(i)
			}
			mgr.Remove(r.tree)
		}
	}

	merged := g.Merged()
	if merged == len(pl.Steps) {
		fmt.Printf("\n%s\n", ui.Success(fmt.Sprintf("✓ parallel workflow complete — all %d steps merged", merged)))
	} else {
		fmt.Printf("\n%s\n", ui.Warn(fmt.Sprintf("■ parallel workflow done — %d/%d steps merged (others rejected, failed, or blocked)", merged, len(pl.Steps))))
	}
	return nil
}

// confirm reads a y/N answer from the shared reader (default no).
func confirm(in *bufio.Reader, question string) bool {
	fmt.Printf("%s [y/N] ", ui.Accent(question))
	line, err := in.ReadString('\n')
	if err != nil {
		fmt.Println()
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
