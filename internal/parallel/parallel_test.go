package parallel

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/engine"
	"github.com/crossben/orchestra-code/internal/gitutil"
	"github.com/crossben/orchestra-code/internal/planner"
	"github.com/crossben/orchestra-code/internal/worktree"
)

func plan(deps ...[]int) planner.Plan {
	var p planner.Plan
	for i, d := range deps {
		p.Steps = append(p.Steps, planner.Step{Title: "s" + StepID(i), DependsOn: d})
	}
	return p
}

func TestGraphWaves(t *testing.T) {
	// 1, 2 independent; 3 after 1; 4 after 3.
	g, err := NewGraph(plan(nil, nil, []int{1}, []int{3}))
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Ready(); len(got) != 2 || got[0] != 0 || got[1] != 1 {
		t.Fatalf("wave 1 = %v", got)
	}
	if n := g.WavesLeft(); n != 3 {
		t.Fatalf("waves left = %d, want 3", n)
	}
	g.MarkDone(0)
	g.MarkDead(1)
	if got := g.Ready(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("wave 2 = %v", got)
	}
	g.MarkDead(2)
	if got := g.Ready(); len(got) != 0 {
		t.Fatalf("nothing should be ready, got %v", got)
	}
	if b := g.Blocked(); len(b) != 1 || b[0] != 3 {
		t.Fatalf("blocked = %v", b)
	}
	if g.Merged() != 1 || g.Len() != 4 {
		t.Fatalf("merged=%d len=%d", g.Merged(), g.Len())
	}
}

func TestStepHelpers(t *testing.T) {
	if StepTask(planner.Step{Title: "t"}) != "t" || StepTask(planner.Step{Title: "t", Detail: "d"}) != "t\nd" {
		t.Fatal("StepTask")
	}
	reg := agent.NewRegistry()
	a := agent.New("alpha", "true", nil, "", nil)
	reg.Add(a)
	reg.Add(agent.New("beta", "true", nil, "", nil))
	reg.Add(agent.New("ghost", "no-such-binary-xyz", nil, "", nil))
	if got := StepAgent(reg, planner.Step{Agent: "beta"}, a, "alpha"); got.Name() != "beta" {
		t.Fatalf("StepAgent = %s", got.Name())
	}
	if got := StepAgent(reg, planner.Step{Agent: "ghost"}, a, "alpha"); got.Name() != "alpha" {
		t.Fatalf("unhealthy agent should fall back, got %s", got.Name())
	}
	if names := HealthyAgentNames(reg); strings.Join(names, ",") != "alpha,beta" {
		t.Fatalf("healthy = %v", names)
	}
}

func gitInit(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
		{"config", "commit.gpgsign", "false"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestBaseGuardPlainFolder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644)
	g := NewBaseGuard(dir)
	if g.InRepo() {
		t.Fatal("plain folder reported as repo")
	}
	if g.Check() {
		t.Fatal("unarmed guard should not discard anything")
	}
	if err := g.Arm(); err != nil {
		t.Fatal(err)
	}
	if g.Check() {
		t.Fatal("clean folder should not trip the guard")
	}
	os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644)
	if !g.Check() {
		t.Fatal("stray write should trip the guard")
	}
	if _, err := os.Stat(filepath.Join(dir, "stray.txt")); !os.IsNotExist(err) {
		t.Fatal("stray write should be discarded")
	}
}

func TestBaseGuardRepo(t *testing.T) {
	dir := gitInit(t)
	g := NewBaseGuard(dir)
	if !g.InRepo() {
		t.Fatal("repo not detected")
	}
	_ = g.Arm()
	os.WriteFile(filepath.Join(dir, "stray.txt"), []byte("x"), 0o644)
	if !g.Check() {
		t.Fatal("stray write should trip the guard")
	}
	if clean, _ := gitutil.IsClean(dir); !clean {
		t.Fatal("repo should be clean again")
	}
}

// RunStep in a git worktree commits the result on the task branch, so the
// isolator can merge it; in a folder copy the changes stay as files.
func TestRunStepCommitsInWorktree(t *testing.T) {
	for _, mode := range []string{"git", "plain"} {
		t.Run(mode, func(t *testing.T) {
			var dir string
			if mode == "git" {
				dir = gitInit(t)
			} else {
				dir = t.TempDir()
			}
			iso, err := worktree.New(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer iso.Cleanup()
			tree, err := iso.Add("1", "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(t.TempDir(), "a.sh")
			os.WriteFile(bin, []byte("#!/bin/sh\necho hi > new.txt\n"), 0o755)
			turn := RunStep(context.Background(), engine.Options{
				Agent: agent.New("a", bin, nil, "", nil), Prompt: "write new.txt", Dir: tree.Dir,
			})
			if turn.Err != nil || !turn.HadChanges {
				t.Fatalf("turn: err=%v changes=%v", turn.Err, turn.HadChanges)
			}
			d, err := iso.Diff(tree)
			if err != nil || !strings.Contains(d, "new.txt") {
				t.Fatalf("isolator diff missing new.txt: %v\n%s", err, d)
			}
			if conflict, err := iso.Merge(tree, "merge"); err != nil || conflict {
				t.Fatalf("merge: conflict=%v err=%v", conflict, err)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "new.txt")); string(b) != "hi\n" {
				t.Fatalf("merged file = %q", b)
			}
		})
	}
}
