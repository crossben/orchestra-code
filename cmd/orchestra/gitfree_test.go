package main

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/config"
	"github.com/crossben/orchestra-code/internal/planner"
)

// fakeAgent writes a shell script and registers it as a CLI agent. The script
// runs in the task directory (or is told it via dirFlag) with the prompt as its
// last argument — exactly how real agent CLIs are driven.
func fakeAgent(t *testing.T, reg *agent.Registry, name, dirFlag, script string) agent.Agent {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name+".sh")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := agent.New(name, bin, nil, dirFlag, []agent.Capability{agent.CapImplement})
	reg.Add(a)
	return a
}

// plainFolder returns a non-git temp dir with one file, set as --dir for the
// duration of the test.
func plainFolder(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := flagDir
	flagDir = dir
	t.Cleanup(func() { flagDir = old })
	return dir
}

func fileIs(t *testing.T, path, want string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(b) != want {
		t.Fatalf("%s = %q, want %q", path, b, want)
	}
}

func twoStepPlan() planner.Plan {
	return planner.Plan{Steps: []planner.Step{
		{Title: "step a", Agent: "alpha"},
		{Title: "step b", Agent: "beta"},
	}}
}

func TestParallelPlainFolderMergesBoth(t *testing.T) {
	dir := plainFolder(t)
	reg := agent.NewRegistry()
	alpha := fakeAgent(t, reg, "alpha", "", `printf 'from alpha\n' > a.txt`)
	// beta ignores cwd and is told its directory through dir_flag ("$2").
	fakeAgent(t, reg, "beta", "--dir", `cd /; mkdir -p "$2/pkg" && printf 'from beta\n' > "$2/pkg/b.txt"`)

	in := bufio.NewReader(strings.NewReader("y\ny\n"))
	if err := runParallel(context.Background(), in, config.Default(), reg, alpha, "alpha", twoStepPlan(), nil, 2, nil); err != nil {
		t.Fatalf("runParallel: %v", err)
	}
	fileIs(t, filepath.Join(dir, "a.txt"), "from alpha\n")
	fileIs(t, filepath.Join(dir, "pkg", "b.txt"), "from beta\n")
	fileIs(t, filepath.Join(dir, "base.txt"), "base\n")
	if _, err := os.Stat(filepath.Join(dir, ".git")); !os.IsNotExist(err) {
		t.Fatal("a plain folder must not become a git repository")
	}
}

func TestParallelPlainFolderConflict(t *testing.T) {
	dir := plainFolder(t)
	reg := agent.NewRegistry()
	alpha := fakeAgent(t, reg, "alpha", "", `printf 'alpha\n' > base.txt; printf 'a\n' > a-only.txt`)
	fakeAgent(t, reg, "beta", "", `printf 'beta\n' > base.txt; printf 'b\n' > b-only.txt`)

	in := bufio.NewReader(strings.NewReader("y\ny\n"))
	if err := runParallel(context.Background(), in, config.Default(), reg, alpha, "alpha", twoStepPlan(), nil, 2, nil); err != nil {
		t.Fatalf("runParallel: %v", err)
	}
	// Step 1 (alpha) merges first; step 2 touched the same file → conflict,
	// and nothing of step 2 lands (all-or-nothing).
	fileIs(t, filepath.Join(dir, "base.txt"), "alpha\n")
	fileIs(t, filepath.Join(dir, "a-only.txt"), "a\n")
	if _, err := os.Stat(filepath.Join(dir, "b-only.txt")); !os.IsNotExist(err) {
		t.Fatal("a conflicting step must not write any of its files")
	}
}

func TestParallelPlainFolderRejectKeepsNothing(t *testing.T) {
	dir := plainFolder(t)
	reg := agent.NewRegistry()
	alpha := fakeAgent(t, reg, "alpha", "", `printf 'a\n' > a.txt`)
	fakeAgent(t, reg, "beta", "", `printf 'b\n' > b.txt`)

	in := bufio.NewReader(strings.NewReader("n\ny\n")) // reject step 1, accept step 2
	if err := runParallel(context.Background(), in, config.Default(), reg, alpha, "alpha", twoStepPlan(), nil, 2, nil); err != nil {
		t.Fatalf("runParallel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Fatal("a rejected step must leave no files behind")
	}
	fileIs(t, filepath.Join(dir, "b.txt"), "b\n")
}

func TestParallelPlainFolderDiscardsStrayWrites(t *testing.T) {
	dir := plainFolder(t)
	reg := agent.NewRegistry()
	// alpha does its job in its copy but also scribbles into the base folder.
	alpha := fakeAgent(t, reg, "alpha", "", `printf 'a\n' > a.txt; printf 'stray\n' > '`+filepath.Join(dir, "stray.txt")+`'`)
	fakeAgent(t, reg, "beta", "", `true`)

	in := bufio.NewReader(strings.NewReader("y\n"))
	if err := runParallel(context.Background(), in, config.Default(), reg, alpha, "alpha", twoStepPlan(), nil, 2, nil); err != nil {
		t.Fatalf("runParallel: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "stray.txt")); !os.IsNotExist(err) {
		t.Fatal("stray write into the base folder should have been discarded")
	}
	fileIs(t, filepath.Join(dir, "a.txt"), "a\n")
}

func TestBenchmarkPlainFolderMergesWinner(t *testing.T) {
	dir := plainFolder(t)
	reg := agent.NewRegistry()
	alpha := fakeAgent(t, reg, "alpha", "", `printf 'alpha wins\n' > win.txt`)
	beta := fakeAgent(t, reg, "beta", "", `true`) // makes no changes

	var ranked []benchResult
	persist := func(_ context.Context, _ string, r []benchResult) { ranked = r }
	in := bufio.NewReader(strings.NewReader("y\n"))
	if err := runBenchmark(context.Background(), in, config.Default(), []agent.Agent{alpha, beta}, "write win.txt", 2, nil, persist); err != nil {
		t.Fatalf("runBenchmark: %v", err)
	}
	if len(ranked) != 2 || ranked[0].agent != "alpha" {
		t.Fatalf("ranking = %+v, want alpha first", ranked)
	}
	if r := ranked[0]; r.err != nil || !r.out.HadChanges || r.files != 1 || r.added != 1 || r.removed != 0 {
		t.Fatalf("alpha result = err %v changes %v files %d +%d -%d", r.err, r.out.HadChanges, r.files, r.added, r.removed)
	}
	fileIs(t, filepath.Join(dir, "win.txt"), "alpha wins\n")
}

func TestBenchmarkPlainFolderDeclineKeepsNothing(t *testing.T) {
	dir := plainFolder(t)
	reg := agent.NewRegistry()
	alpha := fakeAgent(t, reg, "alpha", "", `printf 'x\n' > win.txt`)

	in := bufio.NewReader(strings.NewReader("n\n"))
	if err := runBenchmark(context.Background(), in, config.Default(), []agent.Agent{alpha}, "t", 1, nil, nil); err != nil {
		t.Fatalf("runBenchmark: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "win.txt")); !os.IsNotExist(err) {
		t.Fatal("declining the winner must keep nothing")
	}
}
