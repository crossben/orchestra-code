package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crossben/orchestra-code/internal/agent"
)

func TestBuildRegistryAPIAgent(t *testing.T) {
	yamlSrc := `
default_agent: gpt
agents:
  - name: claude
    bin: claude
    args: ["-p"]
    capabilities: [plan, implement]
  - name: gpt
    type: api
    provider: openai
    model: gpt-4o
    api_key_env: TEST_OPENAI_KEY
    capabilities: [implement]
`
	cfg, err := parseForTest(t, yamlSrc)
	if err != nil {
		t.Fatal(err)
	}
	reg := cfg.BuildRegistry()

	// The API agent is registered and typed.
	a, ok := reg.Get("gpt")
	if !ok {
		t.Fatal("api agent not registered")
	}
	api, isAPI := a.(*agent.APIAgent)
	if !isAPI {
		t.Fatalf("expected *agent.APIAgent, got %T", a)
	}
	if api.Model() != "gpt-4o" || api.ProviderName() != "openai" {
		t.Fatalf("unexpected api fields: model=%s provider=%s", api.Model(), api.ProviderName())
	}

	// CLI agents are untouched.
	c, ok := reg.Get("claude")
	if !ok {
		t.Fatal("cli agent missing")
	}
	if _, isCLI := c.(*agent.CLIAgent); !isCLI {
		t.Fatalf("expected *agent.CLIAgent, got %T", c)
	}
	// The api agent sits alongside the built-in defaults (Load merges over
	// Default), and insertion order is preserved.
	names := reg.Names()
	if len(names) < 2 || names[len(names)-1] != "gpt" {
		t.Fatalf("registry names: %v", names)
	}
}

func TestBuildRegistrySkipsBrokenAPIAgent(t *testing.T) {
	yamlSrc := `
agents:
  - name: broken
    type: api          # no model → construction fails
    provider: openai
`
	cfg, err := parseForTest(t, yamlSrc)
	if err != nil {
		t.Fatal(err)
	}
	reg := cfg.BuildRegistry()
	if _, ok := reg.Get("broken"); ok {
		t.Fatal("misconfigured api agent should be skipped")
	}
}

func TestLoadMergesAPIAgentOverDefaultByName(t *testing.T) {
	dir := t.TempDir()
	src := "agents:\n  - name: claude\n    type: api\n    model: gpt-4o\n    capabilities: [plan]\n"
	if err := writeFile(dir, "orchestra.yaml", src); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, ac := range cfg.Agents {
		if ac.Name == "claude" {
			found = true
			if ac.Type != "api" || ac.Model != "gpt-4o" {
				t.Fatalf("merge did not carry api fields: %+v", ac)
			}
			// Whole-entry replacement: unspecified cli-only fields stay empty.
			if ac.Bin != "" || len(ac.Args) != 0 {
				t.Fatalf("expected clean replacement, got %+v", ac)
			}
		}
	}
	if !found {
		t.Fatal("claude entry lost in merge")
	}
}

func TestDefaultsUnchangedShape(t *testing.T) {
	cfg := Default()
	reg := cfg.BuildRegistry()
	for _, n := range reg.Names() {
		a, _ := reg.Get(n)
		if _, isAPI := a.(*agent.APIAgent); isAPI {
			t.Fatalf("built-in default %q must remain a CLI agent", n)
		}
	}
}

// helpers kept tiny so the test file reads top-down.
func parseForTest(t *testing.T, src string) (*Config, error) {
	t.Helper()
	return loadFromSrc(t.TempDir(), src)
}

func loadFromSrc(dir, src string) (*Config, error) {
	if err := writeFile(dir, "orchestra.yaml", src); err != nil {
		return nil, err
	}
	return Load(dir)
}

func writeFile(dir, name, content string) error {
	return os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644)
}
