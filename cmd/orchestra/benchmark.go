package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/memory"
	"github.com/crossben/orchestra-code/internal/scheduler"
	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/worktree"
	"github.com/spf13/cobra"
)

// benchResult holds one agent's benchmark outcome.
type benchResult struct {
	agent   string
	tree    worktree.Tree
	created bool
	out     engine.Outcome
	dur     time.Duration
	files   int
	added   int
	removed int
	err     error
}

func (r benchResult) valid() bool   { return r.out.Report.Passed() }
func (r benchResult) skipped() bool { return r.out.Report.Skipped }
func (r benchResult) churn() int    { return r.added + r.removed }

// retriesOf reports self-correction retries (attempts beyond the first).
func retriesOf(r benchResult) int {
	if r.out.Attempts <= 1 {
		return 0
	}
	return r.out.Attempts - 1
}

func newBenchmarkCmd() *cobra.Command {
	var (
		only       string
		jobs       int
		compare    bool
		principles string
	)
	cmd := &cobra.Command{
		Use:   `benchmark "<task>"`,
		Short: "Run one task through every agent (isolated) and rank the results",
		Long: "Run the same task from the same starting point through each available agent, each in its own\n" +
			"git worktree, then print a leaderboard (validation, speed, retries, diff size). Offers to keep\n" +
			"the winner. With --compare, runs each agent twice (principles off vs on) and reports how much\n" +
			"smaller the lean-code principles make the diff.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.TrimSpace(strings.Join(args, " "))
			if task == "" {
				return errors.New(`a task is required, e.g. orchestra benchmark "add input validation"`)
			}
			cfg, err := config.Load(flagDir)
			if err != nil {
				return err
			}
			if !gitutil.IsRepo(flagDir) {
				return errNotRepo(flagDir)
			}
			if clean, err := gitutil.IsClean(flagDir); err != nil {
				return err
			} else if !clean {
				return errDirty()
			}

			reg := cfg.BuildRegistry()
			agents := selectAgents(reg, only)
			if len(agents) == 0 {
				return errors.New("no available agents to benchmark (see `orchestra agents`)")
			}

			if compare {
				level := principles
				if level == "" || strings.EqualFold(level, "off") {
					level = "full"
				}
				return runCompare(cmd.Context(), cfg, agents, task, level, jobs)
			}
			if principles != "" {
				cfg.Principles = principles
			}
			if jobs <= 0 {
				jobs = len(agents)
			}

			stages := stagesFor(cfg)
			retries := cfg.RetryLimit()

			mgr, err := worktree.NewManager(flagDir)
			if err != nil {
				return err
			}
			defer mgr.Cleanup()

			names := make([]string, len(agents))
			for i, a := range agents {
				names[i] = a.Name()
			}
			fmt.Printf("%s benchmarking %q across %s\n", ui.Accent("▸"), task, ui.Agent(strings.Join(names, ", ")))

			// Run each agent in its own worktree, in parallel.
			results := make([]benchResult, len(agents))
			scheduler.Bounded(cmd.Context(), jobs, len(agents), func(i int) error {
				a := agents[i]
				results[i].agent = a.Name()
				tree, aerr := mgr.Add("bench-"+a.Name(), "HEAD")
				if aerr != nil {
					results[i].err = aerr
					return aerr
				}
				results[i].tree = tree
				results[i].created = true

				start := time.Now()
				out, rerr := engine.ExecuteHeadless(cmd.Context(), engine.Options{
					Agent:      a,
					Prompt:     task,
					Dir:        tree.Dir,
					Stages:     stages,
					MaxRetries: retries,
					Timeout:    cfg.TimeoutDuration(),
					Memory:     nil, // benchmark records to its own table, not run history
					Label:      "bench:" + a.Name(),
					Principles: config.PrinciplesText(cfg.Principles),
				})
				results[i].dur = time.Since(start)
				results[i].out = out
				results[i].err = rerr
				if rerr == nil && out.HadChanges {
					results[i].files, results[i].added, results[i].removed, _ = mgr.DiffStat(tree)
				}
				return rerr
			})

			ranked := rankResults(results)
			printLeaderboard(ranked)

			// Persist to memory (best-effort).
			persistBenchmarks(cmd.Context(), task, ranked)

			// Offer to keep the winner.
			winner, ok := firstMergeable(ranked)
			if !ok {
				fmt.Println(ui.Dim("\nno agent produced a mergeable result — nothing to keep."))
				return nil
			}
			in := bufio.NewReader(os.Stdin)
			q := fmt.Sprintf("\nmerge winner %s into the base tree?", ui.Agent(winner.agent))
			if !winner.valid() && !winner.skipped() {
				q = fmt.Sprintf("\nwinner %s did NOT pass validation — merge it anyway?", ui.Agent(winner.agent))
			}
			if confirm(in, q) {
				conflict, merr := mgr.Merge(winner.tree, "orchestra: benchmark winner ("+winner.agent+"): "+task)
				if merr != nil {
					return merr
				}
				if conflict {
					fmt.Println(ui.Danger("✗ merge conflict — left unmerged"))
				} else {
					fmt.Println(ui.Success("✓ merged " + winner.agent + "'s result into the base tree"))
				}
			} else {
				fmt.Println(ui.Dim("discarded — no changes kept."))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&only, "agents", "", "comma-separated agents to benchmark (default: all available)")
	cmd.Flags().IntVar(&jobs, "jobs", 0, "max concurrent agents (default: all at once)")
	cmd.Flags().BoolVar(&compare, "compare", false, "run each agent twice (principles off vs on) and report the diff-size reduction")
	cmd.Flags().StringVar(&principles, "principles", "", "principles level: sets the run level, or the 'on' level for --compare (default full)")
	return cmd
}

// runCompare runs each agent twice — principles off vs `level` — and reports how
// much smaller the lean-code principles make the resulting diff. Pure
// measurement: all worktrees are discarded, nothing is merged.
func runCompare(ctx context.Context, cfg *config.Config, agents []agent.Agent, task, level string, jobs int) error {
	onText := config.PrinciplesText(level)
	if onText == "" {
		return fmt.Errorf("--compare needs a non-off principles level (got %q)", level)
	}
	mgr, err := worktree.NewManager(flagDir)
	if err != nil {
		return err
	}
	defer mgr.Cleanup()

	stages := stagesFor(cfg)
	retries := cfg.RetryLimit()

	// One job per (agent, variant).
	type job struct {
		agentIdx int
		variant  string // "off" | "on"
		text     string
	}
	var jobsList []job
	for i := range agents {
		jobsList = append(jobsList, job{i, "off", ""}, job{i, "on", onText})
	}
	if jobs <= 0 {
		jobs = len(jobsList)
	}

	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name()
	}
	fmt.Printf("%s comparing principles off vs %s across %s\n",
		ui.Accent("▸"), ui.Agent(level), ui.Agent(strings.Join(names, ", ")))

	res := make([]benchResult, len(jobsList))
	scheduler.Bounded(ctx, jobs, len(jobsList), func(k int) error {
		j := jobsList[k]
		a := agents[j.agentIdx]
		res[k].agent = a.Name()
		tree, aerr := mgr.Add(fmt.Sprintf("bench-%s-%s", a.Name(), j.variant), "HEAD")
		if aerr != nil {
			res[k].err = aerr
			return aerr
		}
		res[k].tree = tree
		res[k].created = true
		start := time.Now()
		out, rerr := engine.ExecuteHeadless(ctx, engine.Options{
			Agent:      a,
			Prompt:     task,
			Dir:        tree.Dir,
			Stages:     stages,
			MaxRetries: retries,
			Timeout:    cfg.TimeoutDuration(),
			Label:      fmt.Sprintf("%s/%s", a.Name(), j.variant),
			Principles: j.text,
		})
		res[k].dur = time.Since(start)
		res[k].out = out
		res[k].err = rerr
		if rerr == nil && out.HadChanges {
			res[k].files, res[k].added, res[k].removed, _ = mgr.DiffStat(tree)
		}
		return rerr
	})

	// Pair up off/on per agent (jobs are emitted in off,on order).
	pairs := make([]abPair, len(agents))
	for k, j := range jobsList {
		if j.variant == "off" {
			pairs[j.agentIdx].off = res[k]
		} else {
			pairs[j.agentIdx].on = res[k]
		}
	}

	printComparison(agents, pairs, level)
	persistComparison(task, level, pairs, agents)
	return nil
}

// abPair holds an agent's off/on benchmark results.
type abPair struct{ off, on benchResult }

func printComparison(agents []agent.Agent, pairs []abPair, level string) {
	fmt.Printf("\n%s\n", ui.Heading("principles off → "+level))
	fmt.Printf("%-12s %-12s %-10s %-10s %-8s %s\n", "AGENT", "VALID off/on", "±off", "±on", "Δlines", "Δ%")
	var totalOff, totalOn, counted int
	for i, a := range agents {
		p := pairs[i]
		offCh, onCh := churnOf(p.off), churnOf(p.on)
		validOff := validMark(p.off)
		validOn := validMark(p.on)
		delta := onCh - offCh
		pct := "—"
		if p.off.err == nil && p.on.err == nil && p.off.out.HadChanges && p.on.out.HadChanges && offCh > 0 {
			pctVal := 100 * float64(delta) / float64(offCh)
			pct = fmt.Sprintf("%+.0f%%", pctVal)
			totalOff += offCh
			totalOn += onCh
			counted++
		}
		deltaStr := fmt.Sprintf("%+d", delta)
		if delta < 0 {
			deltaStr = ui.Success(deltaStr)
		} else if delta > 0 {
			deltaStr = ui.Danger(deltaStr)
		}
		fmt.Printf("%-12s %-12s %-10s %-10s %-8s %s\n",
			a.Name(), validOff+"/"+validOn,
			churnStr(p.off), churnStr(p.on), deltaStr, pct)
	}
	if counted > 0 && totalOff > 0 {
		avg := 100 * float64(totalOn-totalOff) / float64(totalOff)
		fmt.Printf("\n%s %s fewer changed lines overall (%d → %d across %d agents)\n",
			ui.Accent("▸"), ui.Success(fmt.Sprintf("%+.0f%%", avg)), totalOff, totalOn, counted)
	} else {
		fmt.Println(ui.Dim("\nnot enough comparable results to compute an overall reduction"))
	}
}

func persistComparison(task, level string, pairs []abPair, agents []agent.Agent) {
	mem, err := openMemory()
	if err != nil {
		return
	}
	defer mem.Close()
	dir, _ := absDir()
	now := time.Now()
	rec := func(r benchResult, tag string) {
		_ = mem.RecordBenchmark(memory.BenchRun{
			Dir: dir, Task: task + " (principles=" + tag + ")", Agent: r.agent,
			Valid: r.valid(), Skipped: r.skipped(), Changed: r.out.HadChanges,
			Duration: r.dur, Retries: retriesOf(r),
			Files: r.files, Added: r.added, Removed: r.removed, Exit: r.out.ExitCode, Won: false,
		}, now)
	}
	for i := range agents {
		rec(pairs[i].off, "off")
		rec(pairs[i].on, level)
	}
}

func churnOf(r benchResult) int { return r.added + r.removed }

func churnStr(r benchResult) string {
	if r.err != nil {
		return "err"
	}
	if !r.out.HadChanges {
		return "—"
	}
	return fmt.Sprintf("+%d-%d", r.added, r.removed)
}

func validMark(r benchResult) string {
	switch {
	case r.err != nil:
		return "e"
	case r.skipped():
		return "?"
	case r.valid():
		return "✓"
	default:
		return "✗"
	}
}

// selectAgents returns healthy agents, optionally filtered by a comma list.
func selectAgents(reg *agent.Registry, only string) []agent.Agent {
	var filter map[string]bool
	if strings.TrimSpace(only) != "" {
		filter = map[string]bool{}
		for _, n := range strings.Split(only, ",") {
			filter[strings.TrimSpace(n)] = true
		}
	}
	var out []agent.Agent
	for _, a := range reg.All() {
		if filter != nil && !filter[a.Name()] {
			continue
		}
		if a.Health() != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// rankResults sorts best-first: valid > made-changes > fewer retries > faster > smaller diff.
func rankResults(results []benchResult) []benchResult {
	ranked := make([]benchResult, len(results))
	copy(ranked, results)
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if a.valid() != b.valid() {
			return a.valid()
		}
		if a.out.HadChanges != b.out.HadChanges {
			return a.out.HadChanges
		}
		if a.out.Attempts != b.out.Attempts {
			return a.out.Attempts < b.out.Attempts
		}
		if a.dur != b.dur {
			return a.dur < b.dur
		}
		return a.churn() < b.churn()
	})
	return ranked
}

// firstMergeable returns the top-ranked result that actually produced changes.
func firstMergeable(ranked []benchResult) (benchResult, bool) {
	for _, r := range ranked {
		if r.err == nil && r.out.HadChanges {
			return r, true
		}
	}
	return benchResult{}, false
}

func printLeaderboard(ranked []benchResult) {
	fmt.Printf("\n%s\n", ui.Heading("leaderboard"))
	fmt.Printf("%-4s %-12s %-8s %-8s %-8s %-6s %s\n", "RANK", "AGENT", "VALID", "TIME", "RETRIES", "FILES", "±LINES")
	for i, r := range ranked {
		rank := fmt.Sprintf("%d", i+1)
		if i == 0 {
			rank = "1 ★"
		}
		valid := ui.Dim("—")
		switch {
		case r.err != nil:
			valid = ui.Danger("err")
		case r.skipped():
			valid = ui.Dim("n/a")
		case r.valid():
			valid = ui.Success("✓")
		default:
			valid = ui.Danger("✗")
		}
		lines := "—"
		if r.out.HadChanges {
			lines = fmt.Sprintf("+%d -%d", r.added, r.removed)
		}
		detail := ""
		if r.err != nil {
			detail = "  " + ui.Dim(firstLine(r.err.Error()))
		} else if !r.out.HadChanges {
			detail = "  " + ui.Dim("(no changes)")
		}
		fmt.Printf("%-4s %-12s %-8s %-8s %-8d %-6d %s%s\n",
			rank, r.agent, valid,
			r.dur.Round(time.Second/10).String(), retriesOf(r), r.files, lines, detail)
	}
}

func persistBenchmarks(_ context.Context, task string, ranked []benchResult) {
	mem, err := openMemory()
	if err != nil {
		return
	}
	defer mem.Close()
	dir, _ := absDir()
	now := time.Now()
	for i, r := range ranked {
		_ = mem.RecordBenchmark(memory.BenchRun{
			Dir: dir, Task: task, Agent: r.agent,
			Valid: r.valid(), Skipped: r.skipped(), Changed: r.out.HadChanges,
			Duration: r.dur, Retries: retriesOf(r),
			Files: r.files, Added: r.added, Removed: r.removed,
			Exit: r.out.ExitCode, Won: i == 0,
		}, now)
	}
}
