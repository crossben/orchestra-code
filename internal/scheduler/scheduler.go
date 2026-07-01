// Package scheduler provides bounded-concurrency execution and the dependency
// bookkeeping that drives Orchestra's wave-based parallel workflows: run every
// step whose dependencies are satisfied at once, then unlock the next wave.
package scheduler

import (
	"context"
	"fmt"
	"sync"
)

// Bounded runs n tasks with at most `jobs` running concurrently, returning each
// task's error by index. A cancelled context short-circuits not-yet-started
// tasks with ctx.Err().
func Bounded(ctx context.Context, jobs, n int, fn func(i int) error) []error {
	if jobs < 1 {
		jobs = 1
	}
	errs := make([]error, n)
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			errs[i] = fn(i)
		}(i)
	}
	wg.Wait()
	return errs
}

// Node is a task in a dependency graph, identified by ID with prerequisite IDs.
type Node struct {
	ID   string
	Deps []string
}

// Validate reports a cycle or a reference to an unknown dependency.
func Validate(nodes []Node) error {
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	for _, n := range nodes {
		for _, d := range n.Deps {
			if !ids[d] {
				return fmt.Errorf("step %q depends on unknown step %q", n.ID, d)
			}
		}
	}
	// Kahn's algorithm for cycle detection.
	indeg := map[string]int{}
	adj := map[string][]string{}
	for _, n := range nodes {
		indeg[n.ID] += 0
		for _, d := range n.Deps {
			indeg[n.ID]++
			adj[d] = append(adj[d], n.ID)
		}
	}
	var queue []string
	for id, d := range indeg {
		if d == 0 {
			queue = append(queue, id)
		}
	}
	visited := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		visited++
		for _, next := range adj[id] {
			indeg[next]--
			if indeg[next] == 0 {
				queue = append(queue, next)
			}
		}
	}
	if visited != len(nodes) {
		return fmt.Errorf("dependency cycle detected among steps")
	}
	return nil
}

// Ready returns the indices of nodes that can run now: not yet done, not dead,
// and with every dependency already done. A node whose dependency is dead
// (failed/rejected) is itself considered blocked and never becomes ready.
func Ready(nodes []Node, done, dead map[string]bool) (ready []int) {
	for i, n := range nodes {
		if done[n.ID] || dead[n.ID] {
			continue
		}
		if blockedByDead(n, dead) {
			continue
		}
		ok := true
		for _, d := range n.Deps {
			if !done[d] {
				ok = false
				break
			}
		}
		if ok {
			ready = append(ready, i)
		}
	}
	return ready
}

// Blocked returns nodes that can never run because a dependency (transitively)
// is dead. Used to report what got skipped after a rejection.
func Blocked(nodes []Node, done, dead map[string]bool) (blocked []int) {
	for i, n := range nodes {
		if done[n.ID] || dead[n.ID] {
			continue
		}
		if blockedByDead(n, dead) {
			blocked = append(blocked, i)
		}
	}
	return blocked
}

func blockedByDead(n Node, dead map[string]bool) bool {
	for _, d := range n.Deps {
		if dead[d] {
			return true
		}
	}
	return false
}
