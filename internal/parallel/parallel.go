// Package parallel holds the UI-free pieces of Orchestra's parallel workflow,
// shared by `orchestra do --parallel` and the dashboard: step naming and agent
// choice, the dependency graph that yields waves, the base-tree guard against
// agents writing outside their isolated tree, and the quiet per-task pipeline.
//
// Isolation itself lives in internal/worktree (git worktrees or folder copies)
// and the supervised loop in internal/engine; this package only wires them.
package parallel

import (
	"context"
	"fmt"
	"strconv"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/fsdiff"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/planner"
	"github.com/crossben/orchestra-code/internal/scheduler"
)

// DefaultJobs is the default bound on concurrently running steps.
const DefaultJobs = 4

// StepID is the scheduler ID of the step at index i (1-based, as planners
// number them in depends_on).
func StepID(i int) string { return strconv.Itoa(i + 1) }

// StepTask is the prompt an agent receives for a step.
func StepTask(step planner.Step) string {
	if step.Detail == "" {
		return step.Title
	}
	return step.Title + "\n" + step.Detail
}

// StepAgent honors a planner-assigned per-step agent when valid+healthy,
// otherwise falls back to the workflow agent.
func StepAgent(reg *agent.Registry, step planner.Step, fallback agent.Agent, fallbackName string) agent.Agent {
	if step.Agent != "" && step.Agent != fallbackName {
		if a, ok := reg.Get(step.Agent); ok && a.Health() == nil {
			return a
		}
	}
	return fallback
}

// HealthyAgentNames returns the names of installed/available agents, offered
// to the planner as per-step agent choices.
func HealthyAgentNames(reg *agent.Registry) []string {
	var names []string
	for _, a := range reg.All() {
		if a.Health() == nil {
			names = append(names, a.Name())
		}
	}
	return names
}

// Graph is a plan's dependency graph plus the progress made on it: steps that
// are done (merged, or finished without changes) and dead (failed, rejected,
// conflicting). Dead steps block their dependents.
type Graph struct {
	nodes      []scheduler.Node
	done, dead map[string]bool
}

// NewGraph builds and validates the graph for pl.
func NewGraph(pl planner.Plan) (*Graph, error) {
	nodes := make([]scheduler.Node, len(pl.Steps))
	for i, s := range pl.Steps {
		deps := make([]string, 0, len(s.DependsOn))
		for _, d := range s.DependsOn {
			deps = append(deps, strconv.Itoa(d))
		}
		nodes[i] = scheduler.Node{ID: StepID(i), Deps: deps}
	}
	if err := scheduler.Validate(nodes); err != nil {
		return nil, err
	}
	return &Graph{nodes: nodes, done: map[string]bool{}, dead: map[string]bool{}}, nil
}

// Len is the number of steps.
func (g *Graph) Len() int { return len(g.nodes) }

// Ready returns the indices of steps that can run now.
func (g *Graph) Ready() []int { return scheduler.Ready(g.nodes, g.done, g.dead) }

// Blocked returns the indices of steps that can never run (a dependency died).
func (g *Graph) Blocked() []int { return scheduler.Blocked(g.nodes, g.done, g.dead) }

// MarkDone records step i as done (its dependents may run).
func (g *Graph) MarkDone(i int) { g.done[StepID(i)] = true }

// MarkDead records step i as failed/rejected (its dependents are blocked).
func (g *Graph) MarkDead(i int) { g.dead[StepID(i)] = true }

// Merged is the number of steps done so far.
func (g *Graph) Merged() int { return len(g.done) }

// WavesLeft is how many waves remain, counting the one Ready would start now,
// if every pending step succeeds. It shrinks as steps die.
func (g *Graph) WavesLeft() int {
	done := make(map[string]bool, len(g.done))
	for k, v := range g.done {
		done[k] = v
	}
	n := 0
	for {
		ready := scheduler.Ready(g.nodes, done, g.dead)
		if len(ready) == 0 {
			return n
		}
		n++
		for _, i := range ready {
			done[StepID(i)] = true
		}
	}
}

// BaseGuard catches agents that wrote into the base directory instead of their
// isolated tree (some CLIs don't honor the working directory) and discards
// that stray work. Inside a repository "stray" means the tree is no longer
// clean; in a plain folder it means the folder differs from a snapshot armed
// before the wave's fan-out.
type BaseGuard struct {
	dir    string
	inRepo bool
	snap   *fsdiff.Snapshot
}

// NewBaseGuard returns a guard for dir.
func NewBaseGuard(dir string) *BaseGuard {
	return &BaseGuard{dir: dir, inRepo: gitutil.IsRepo(dir)}
}

// InRepo reports whether the guarded directory is a git repository.
func (g *BaseGuard) InRepo() bool { return g.inRepo }

// Arm records the base state before a wave runs (a no-op inside a
// repository, where the clean tree is the reference).
func (g *BaseGuard) Arm() error {
	if g.inRepo {
		return nil
	}
	snap, err := fsdiff.Capture(g.dir)
	if err != nil {
		return fmt.Errorf("snapshot %s: %w", g.dir, err)
	}
	g.snap = snap
	return nil
}

// Check discards any change made to the base since Arm, reporting whether it
// did.
func (g *BaseGuard) Check() bool {
	if g.inRepo {
		if clean, _ := gitutil.IsClean(g.dir); !clean {
			_ = gitutil.Restore(g.dir)
			return true
		}
		return false
	}
	if g.snap == nil {
		return false
	}
	if d, err := fsdiff.Diff(g.dir, g.snap); err != nil || d == "" {
		return false
	}
	_ = fsdiff.Restore(g.dir, g.snap)
	return true
}

// RunStep is the quiet per-task pipeline for UIs: dispatch → validate →
// self-correct in the isolated tree opts.Dir (engine.Produce, so OnEvent and
// Output work), then — when the tree is a git worktree and the task changed
// something — commit on the task branch so the isolator can merge it. Outside
// git the changes simply stay in the folder copy. Nothing is printed and
// nothing reaches the base tree: that is the isolator's Merge, on accept.
func RunStep(ctx context.Context, opts engine.Options) engine.Turn {
	t := engine.Produce(ctx, opts)
	if t.Err != nil || !t.HadChanges || ctx.Err() != nil {
		return t
	}
	if _, err := engine.CommitAccepted(opts.Dir, engine.CommitMessage(opts.Prompt)); err != nil {
		t.Err = fmt.Errorf("commit changes: %w", err)
	}
	return t
}
