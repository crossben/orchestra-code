package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectGo(t *testing.T) {
	if !have("go") {
		t.Skip("go not on PATH")
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n\ngo 1.22\n")
	stages := Detect(dir)
	if len(stages) == 0 || stages[0].Name != "build" {
		t.Fatalf("expected go stages, got %v", stages)
	}
}

func TestDetectNodeScriptsOnly(t *testing.T) {
	if !have("npm") {
		t.Skip("npm not on PATH")
	}
	dir := t.TempDir()
	// Only a "test" script exists → only a test stage.
	write(t, dir, "package.json", `{"scripts":{"test":"echo ok"}}`)
	stages := Detect(dir)
	if len(stages) != 1 || stages[0].Name != "test" {
		t.Fatalf("expected only a test stage, got %v", stages)
	}
}

func TestDetectGenericJS(t *testing.T) {
	if !have("node") {
		t.Skip("node not on PATH")
	}
	dir := t.TempDir()
	write(t, dir, "app.js", "console.log(1)\n")
	stages := Detect(dir)
	if len(stages) != 1 || stages[0].Name != "syntax" {
		t.Fatalf("expected a syntax stage, got %v", stages)
	}
}

func TestDetectNothing(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "notes.txt", "hello")
	if stages := Detect(dir); len(stages) != 0 {
		t.Fatalf("expected no stages, got %v", stages)
	}
}

func TestResolvePrefersExplicit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "go.mod", "module x\n")
	c := &Config{Validate: ValidateConfig{Test: "custom test"}}
	stages, detected := c.ResolveStages(dir)
	if detected {
		t.Fatal("explicit config should not be marked detected")
	}
	if len(stages) != 1 || stages[0].Command != "custom test" {
		t.Fatalf("expected explicit test stage, got %v", stages)
	}
}

func TestResolveAutoOff(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "app.js", "1")
	off := false
	c := &Config{Validate: ValidateConfig{Auto: &off}}
	if stages, _ := c.ResolveStages(dir); len(stages) != 0 {
		t.Fatalf("auto=false should yield no stages, got %v", stages)
	}
}
