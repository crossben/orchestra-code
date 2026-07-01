// Package config loads Orchestra's YAML configuration and provides the built-in
// agent defaults. A project need not ship a config file at all — the defaults
// cover the common agents; a config file overrides defaults and adds agents.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/crossben/orchestra/internal/agent"
	"github.com/crossben/orchestra/internal/validate"
	"gopkg.in/yaml.v3"
)

// ConfigFileNames are the filenames Load looks for, in order.
var ConfigFileNames = []string{"orchestra.yaml", "orchestra.yml", ".orchestra.yaml"}

// AgentConfig describes how to invoke one agent CLI.
type AgentConfig struct {
	Name         string   `yaml:"name"`
	Bin          string   `yaml:"bin"`          // binary on PATH (defaults to Name)
	Args         []string `yaml:"args"`         // headless/auto-approve prefix; task appended
	Capabilities []string `yaml:"capabilities"` // plan|implement|review
}

// ValidateConfig configures the validation pipeline. Any empty stage is skipped.
type ValidateConfig struct {
	Build string `yaml:"build"`
	Lint  string `yaml:"lint"`
	Test  string `yaml:"test"`
}

// Config is the top-level Orchestra configuration.
type Config struct {
	DefaultAgent string         `yaml:"default_agent"`
	TestCommand  string         `yaml:"test_command"` // shorthand for validate.test (back-compat)
	Validate     ValidateConfig `yaml:"validate"`
	MaxRetries   *int           `yaml:"max_retries"` // pointer so 0 (disable) differs from unset
	Timeout      string         `yaml:"timeout"`     // Go duration string, e.g. "10m"
	Agents       []AgentConfig  `yaml:"agents"`
}

// Stages returns the ordered, non-empty validation stages. The legacy
// test_command is used as the test stage when validate.test is unset.
func (c *Config) Stages() []validate.Stage {
	test := c.Validate.Test
	if test == "" {
		test = c.TestCommand
	}
	candidates := []validate.Stage{
		{Name: "build", Command: c.Validate.Build},
		{Name: "lint", Command: c.Validate.Lint},
		{Name: "test", Command: test},
	}
	var out []validate.Stage
	for _, s := range candidates {
		if s.Command != "" {
			out = append(out, s)
		}
	}
	return out
}

// RetryLimit returns the max number of self-correction retries (default 2).
func (c *Config) RetryLimit() int {
	if c.MaxRetries == nil {
		return 2
	}
	if *c.MaxRetries < 0 {
		return 0
	}
	return *c.MaxRetries
}

// TimeoutDuration parses Timeout, falling back to 10m if unset/invalid.
func (c *Config) TimeoutDuration() time.Duration {
	if c.Timeout == "" {
		return 10 * time.Minute
	}
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 10 * time.Minute
	}
	return d
}

// Default returns the built-in configuration: the common agents in headless,
// auto-approve mode. Orchestra's diff review is the human gate, so each agent is
// told to skip its own permission prompts.
//
// NOTE: the auto-approve flags for claude and opencode are verified against the
// installed CLIs; codex and gemini flags are best-effort defaults — override in
// orchestra.yaml if your version differs.
func Default() *Config {
	two := 2
	return &Config{
		DefaultAgent: "claude",
		Timeout:      "10m",
		MaxRetries:   &two,
		Agents: []AgentConfig{
			{
				Name:         "claude",
				Bin:          "claude",
				Args:         []string{"-p", "--dangerously-skip-permissions"},
				Capabilities: []string{"plan", "implement", "review"},
			},
			{
				Name:         "opencode",
				Bin:          "opencode",
				Args:         []string{"run", "--dangerously-skip-permissions"},
				Capabilities: []string{"implement", "review"},
			},
			{
				Name:         "mimo",
				Bin:          "mimo",
				Args:         []string{"run", "--dangerously-skip-permissions"},
				Capabilities: []string{"implement", "review"},
			},
			{
				Name:         "codex",
				Bin:          "codex",
				Args:         []string{"exec", "--dangerously-bypass-approvals-and-sandbox"},
				Capabilities: []string{"implement"},
			},
			{
				Name:         "gemini",
				Bin:          "gemini",
				Args:         []string{"-p", "--yolo"},
				Capabilities: []string{"plan", "review"},
			},
		},
	}
}

// Load reads the first config file found in dir, merged over the built-in
// defaults. Missing file is not an error — the defaults are returned.
func Load(dir string) (*Config, error) {
	cfg := Default()

	path := findConfig(dir)
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	var user Config
	if err := yaml.Unmarshal(data, &user); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	merge(cfg, &user)
	return cfg, nil
}

func findConfig(dir string) string {
	for _, name := range ConfigFileNames {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// merge overlays user settings onto the defaults. Agents with a name matching a
// default replace it; new names are appended.
func merge(base, user *Config) {
	if user.DefaultAgent != "" {
		base.DefaultAgent = user.DefaultAgent
	}
	if user.TestCommand != "" {
		base.TestCommand = user.TestCommand
	}
	if user.Validate.Build != "" {
		base.Validate.Build = user.Validate.Build
	}
	if user.Validate.Lint != "" {
		base.Validate.Lint = user.Validate.Lint
	}
	if user.Validate.Test != "" {
		base.Validate.Test = user.Validate.Test
	}
	if user.MaxRetries != nil {
		base.MaxRetries = user.MaxRetries
	}
	if user.Timeout != "" {
		base.Timeout = user.Timeout
	}
	for _, ua := range user.Agents {
		replaced := false
		for i, ba := range base.Agents {
			if ba.Name == ua.Name {
				base.Agents[i] = ua
				replaced = true
				break
			}
		}
		if !replaced {
			base.Agents = append(base.Agents, ua)
		}
	}
}

// BuildRegistry turns the config's agents into a live agent.Registry.
func (c *Config) BuildRegistry() *agent.Registry {
	reg := agent.NewRegistry()
	for _, ac := range c.Agents {
		bin := ac.Bin
		if bin == "" {
			bin = ac.Name
		}
		caps := make([]agent.Capability, 0, len(ac.Capabilities))
		for _, cp := range ac.Capabilities {
			caps = append(caps, agent.Capability(cp))
		}
		reg.Add(agent.New(ac.Name, bin, ac.Args, caps))
	}
	return reg
}
