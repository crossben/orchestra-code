package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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
	if in, out := api.Pricing(); in != 0 || out != 0 {
		t.Fatalf("no price fields → unpriced, got %v/%v", in, out)
	}
}

func TestBuildRegistryAPIAgentPricing(t *testing.T) {
	cfg, err := parseForTest(t, `
agents:
  - name: sonnet
    type: api
    provider: anthropic
    model: claude-sonnet
    price_input_per_mtok: 3
    price_output_per_mtok: 15.5
`)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := cfg.BuildRegistry().Get("sonnet")
	api, ok := a.(*agent.APIAgent)
	if !ok {
		t.Fatalf("expected *agent.APIAgent, got %T", a)
	}
	if in, out := api.Pricing(); in != 3 || out != 15.5 {
		t.Fatalf("pricing = %v/%v, want 3/15.5", in, out)
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

func TestBuildRegistryPassesEnvBlock(t *testing.T) {
	t.Setenv("BYOK_CFG_GROQ", "")
	t.Setenv("BYOK_CFG_WORK", "sk-work")
	cfg, err := parseForTest(t, `
agents:
  - name: opencode-groq
    bin: true
    env:
      GROQ_API_KEY: "${BYOK_CFG_GROQ}"
  - name: sonnet
    type: api
    provider: anthropic
    model: m
    api_key_env: BYOK_CFG_UNUSED
    env:
      BYOK_CFG_UNUSED: "${BYOK_CFG_WORK}"
`)
	if err != nil {
		t.Fatal(err)
	}
	reg := cfg.BuildRegistry()
	cli, _ := reg.Get("opencode-groq")
	var ee *agent.EnvError
	if err := cli.Health(); !errors.As(err, &ee) || ee.Var != "BYOK_CFG_GROQ" {
		t.Fatalf("cli agent should report the unset reference, got %v", err)
	}
	api, _ := reg.Get("sonnet")
	if err := api.Health(); err != nil {
		t.Fatalf("api agent key should come from env block: %v", err)
	}
}

func TestLiteralSecretWarnings(t *testing.T) {
	cfg, err := parseForTest(t, `
agents:
  - name: a
    env:
      GROQ_API_KEY: "gsk_live_abc"
      MY_TOKEN: "${FROM_ENV}"
      LOG_LEVEL: "debug"
`)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.LiteralSecretWarnings()
	if len(w) != 1 || !strings.Contains(w[0], "GROQ_API_KEY") || strings.Contains(w[0], "gsk_live_abc") {
		t.Fatalf("want one warning naming (not echoing) the literal key, got %v", w)
	}
}
