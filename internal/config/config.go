// Package config loads Orchestra's YAML configuration and provides the built-in
// agent defaults. A project need not ship a config file at all — the defaults
// cover the common agents; a config file overrides defaults and adds agents.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/agent"
	"github.com/crossben/orchestra-code/internal/router"
	"github.com/crossben/orchestra-code/internal/validate"
	"gopkg.in/yaml.v3"
)

// ConfigFileNames are the filenames Load looks for, in order.
var ConfigFileNames = []string{"orchestra.yaml", "orchestra.yml", ".orchestra.yaml"}

// AgentConfig describes how to invoke one agent CLI.
type AgentConfig struct {
	Name         string   `yaml:"name"`
	Bin          string   `yaml:"bin"`          // binary on PATH (defaults to Name)
	Args         []string `yaml:"args"`         // headless/auto-approve prefix; task appended
	DirFlag      string   `yaml:"dir_flag"`     // flag to pass the working dir (for CLIs that ignore cwd, e.g. opencode "--dir")
	Capabilities []string `yaml:"capabilities"` // plan|implement|review
}

// ValidateConfig configures the validation pipeline. Any empty stage is skipped.
type ValidateConfig struct {
	Build string `yaml:"build"`
	Lint  string `yaml:"lint"`
	Test  string `yaml:"test"`
	// Auto enables ecosystem auto-detection of validators when no explicit
	// stages are set (default true). Pointer so "false" differs from unset.
	Auto *bool `yaml:"auto"`
}

// RouterConfig configures the AI routing layer.
type RouterConfig struct {
	Enabled *bool             `yaml:"enabled"` // pointer so "false" differs from unset
	Agent   string            `yaml:"agent"`   // agent that does classification/answers
	Routes  map[string]string `yaml:"routes"`  // intent → agent name (static fallback)
}

// Config is the top-level Orchestra configuration.
type Config struct {
	DefaultAgent string         `yaml:"default_agent"`
	TestCommand  string         `yaml:"test_command"` // shorthand for validate.test (back-compat)
	Validate     ValidateConfig `yaml:"validate"`
	MaxRetries   *int           `yaml:"max_retries"` // pointer so 0 (disable) differs from unset
	Router       RouterConfig   `yaml:"router"`
	Principles   string         `yaml:"principles"` // lean-code preamble intensity: off | lite | full
	Timeout      string         `yaml:"timeout"`    // Go duration string, e.g. "10m"
	Agents       []AgentConfig  `yaml:"agents"`
}

// Lean-code "principles" preambles (inspired by simplicity-first / anti-over-
// engineering practice). Injected into every task so any agent produces leaner
// changes, agent-agnostically.
const (
	principlesLite = "Prefer the simplest solution that works. Reuse what's already in the codebase and the " +
		"standard library before adding anything; avoid new dependencies, speculative abstraction, and " +
		"features that weren't asked for. Keep the change small.\n\n"

	principlesFull = "Follow a strict simplicity-first discipline. Before writing code, in order: " +
		"(1) question whether it needs to exist at all (YAGNI); (2) reuse existing code in this repo; " +
		"(3) use the standard library; (4) use a native platform/runtime feature; (5) use an " +
		"already-installed dependency; (6) prefer a small one-liner; (7) only then write the minimum " +
		"custom code needed. Add no new dependencies, speculative abstractions, or unrequested features. " +
		"Keep the diff as small as possible while preserving correctness, error handling, and security.\n\n"
)

// PrinciplesText returns the preamble for an intensity level ("" for off/unknown).
func PrinciplesText(level string) string {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "full":
		return principlesFull
	case "lite":
		return principlesLite
	default:
		return ""
	}
}

// RouterEnabled reports whether the AI router is on (default true).
func (c *Config) RouterEnabled() bool {
	return c.Router.Enabled == nil || *c.Router.Enabled
}

// RouterAgent returns the classification agent (falls back to default_agent).
func (c *Config) RouterAgent() string {
	if c.Router.Agent != "" {
		return c.Router.Agent
	}
	return c.DefaultAgent
}

// explicitStages returns the ordered validation stages configured by hand. The
// legacy test_command is used as the test stage when validate.test is unset.
func (c *Config) explicitStages() []validate.Stage {
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

// autoDetect reports whether validator auto-detection is enabled (default true).
func (c *Config) autoDetect() bool {
	return c.Validate.Auto == nil || *c.Validate.Auto
}

// ResolveStages returns the validation stages to run for dir: hand-configured
// stages if any, otherwise ecosystem-detected ones (unless auto-detect is off).
// The bool reports whether the stages came from auto-detection.
func (c *Config) ResolveStages(dir string) ([]validate.Stage, bool) {
	if explicit := c.explicitStages(); len(explicit) > 0 {
		return explicit, false
	}
	if c.autoDetect() {
		return Detect(dir), true
	}
	return nil, false
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
	enabled := true
	return &Config{
		DefaultAgent: "claude",
		Timeout:      "10m",
		MaxRetries:   &two,
		Principles:   "lite", // gentle lean-code nudge on by default

		Router: RouterConfig{
			Enabled: &enabled,
			Agent:   "claude",
			Routes: map[string]string{
				"plan":      "claude",
				"implement": "opencode",
				"review":    "claude",
			},
		},
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
				DirFlag:      "--dir", // opencode ignores process cwd; must be told its dir
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
	if user.Principles != "" {
		base.Principles = user.Principles
	}
	if user.Router.Enabled != nil {
		base.Router.Enabled = user.Router.Enabled
	}
	if user.Router.Agent != "" {
		base.Router.Agent = user.Router.Agent
	}
	for intent, ag := range user.Router.Routes {
		if base.Router.Routes == nil {
			base.Router.Routes = map[string]string{}
		}
		base.Router.Routes[intent] = ag
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

// BuildRouter constructs the AI router from config against a registry. The
// router (CLI classifier) uses the configured router agent for classification
// and direct answers.
func (c *Config) BuildRouter(reg *agent.Registry) (*router.Router, error) {
	ra, ok := reg.Get(c.RouterAgent())
	if !ok {
		return nil, fmt.Errorf("router agent %q is not configured", c.RouterAgent())
	}
	answerer, ok := ra.(agent.Querier)
	if !ok {
		return nil, fmt.Errorf("router agent %q cannot answer questions (no query support)", c.RouterAgent())
	}
	cls, err := router.NewCLIClassifier(ra, reg.Names(), c.TimeoutDuration())
	if err != nil {
		return nil, err
	}
	return router.New(cls, answerer, reg, c.Router.Routes, c.DefaultAgent), nil
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
		reg.Add(agent.New(ac.Name, bin, ac.Args, ac.DirFlag, caps))
	}
	return reg
}
