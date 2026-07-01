package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBoundedRunsAllWithinCap(t *testing.T) {
	const n, jobs = 20, 4
	var running, maxRunning int32
	var mu sync.Mutex
	done := make([]bool, n)

	errs := Bounded(context.Background(), jobs, n, func(i int) error {
		cur := atomic.AddInt32(&running, 1)
		for {
			m := atomic.LoadInt32(&maxRunning)
			if cur <= m || atomic.CompareAndSwapInt32(&maxRunning, m, cur) {
				break
			}
		}
		// tiny busy spin to overlap
		for j := 0; j < 10000; j++ {
			_ = j
		}
		atomic.AddInt32(&running, -1)
		mu.Lock()
		done[i] = true
		mu.Unlock()
		return nil
	})

	for i, e := range errs {
		if e != nil {
			t.Fatalf("task %d errored: %v", i, e)
		}
		if !done[i] {
			t.Fatalf("task %d did not run", i)
		}
	}
	if maxRunning > jobs {
		t.Fatalf("concurrency cap exceeded: sawmax=%d jobs=%d", maxRunning, jobs)
	}
}

func TestValidateDetectsCycle(t *testing.T) {
	nodes := []Node{
		{ID: "1", Deps: []string{"3"}},
		{ID: "2", Deps: []string{"1"}},
		{ID: "3", Deps: []string{"2"}},
	}
	if err := Validate(nodes); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestValidateDetectsUnknownDep(t *testing.T) {
	nodes := []Node{{ID: "1", Deps: []string{"9"}}}
	if err := Validate(nodes); err == nil {
		t.Fatal("expected unknown-dep error, got nil")
	}
}

func TestValidateAcceptsDAG(t *testing.T) {
	nodes := []Node{
		{ID: "1"},
		{ID: "2"},
		{ID: "3", Deps: []string{"1", "2"}},
	}
	if err := Validate(nodes); err != nil {
		t.Fatalf("valid DAG rejected: %v", err)
	}
}

func TestReadyAndBlocked(t *testing.T) {
	nodes := []Node{
		{ID: "1"},
		{ID: "2", Deps: []string{"1"}},
		{ID: "3", Deps: []string{"1"}},
	}
	// Nothing done yet → only step 1 is ready.
	ready := Ready(nodes, map[string]bool{}, map[string]bool{})
	if len(ready) != 1 || ready[0] != 0 {
		t.Fatalf("expected only step 1 ready, got %v", ready)
	}
	// Step 1 done → 2 and 3 ready.
	ready = Ready(nodes, map[string]bool{"1": true}, map[string]bool{})
	if len(ready) != 2 {
		t.Fatalf("expected steps 2,3 ready, got %v", ready)
	}
	// Step 1 dead → 2 and 3 blocked, none ready.
	dead := map[string]bool{"1": true}
	if r := Ready(nodes, map[string]bool{}, dead); len(r) != 0 {
		t.Fatalf("expected nothing ready when dep dead, got %v", r)
	}
	if b := Blocked(nodes, map[string]bool{}, dead); len(b) != 2 {
		t.Fatalf("expected 2 blocked, got %v", b)
	}
}
