package agent

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
)

// EnvError reports a variable an agent needs from the environment that is
// unset or empty — typically a missing API key. Callers use errors.As to tell
// "missing key" apart from "not installed".
type EnvError struct {
	Agent string // agent name
	Var   string // the environment variable that is missing
	For   string // the env: entry that references it; "" when Var is the API key itself
}

func (e *EnvError) Error() string {
	if e.For != "" {
		return fmt.Sprintf("%s is not set (referenced by env.%s of agent %q)", e.Var, e.For, e.Agent)
	}
	return fmt.Sprintf("%s is not set: export it, or point api_key_env / env at the variable holding your key (agent %q)", e.Var, e.Agent)
}

// expandEnv resolves an agent's env: block against the process environment.
// Values may reference variables as $VAR or ${VAR}. It returns sorted
// KEY=value pairs for the child process, plus every referenced variable that
// is unset or empty (in key order), so Health can report them without the
// values ever being printed.
func expandEnv(env map[string]string) (pairs []string, missing []EnvError) {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := os.Expand(env[k], func(name string) string {
			val := os.Getenv(name)
			if strings.TrimSpace(val) == "" {
				missing = append(missing, EnvError{Var: name, For: k})
			}
			return val
		})
		pairs = append(pairs, k+"="+v)
	}
	return pairs, missing
}

// firstMissing turns the first unresolved reference into an error for agent.
func firstMissing(agent string, missing []EnvError) error {
	if len(missing) == 0 {
		return nil
	}
	e := missing[0]
	e.Agent = agent
	return &e
}

// copyEnv defensively copies an env: block.
func copyEnv(env map[string]string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		out[k] = v
	}
	return out
}

// HealthLabel is a short status for a Health result: "available",
// "missing key" (an *EnvError), or "not installed".
func HealthLabel(err error) string {
	var ee *EnvError
	switch {
	case err == nil:
		return "available"
	case errors.As(err, &ee):
		return "missing key"
	default:
		return "not installed"
	}
}
