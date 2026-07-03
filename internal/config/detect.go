package config

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/crossben/orchestra-code/internal/validate"
)

// Detect inspects a project directory and returns sensible default validation
// stages for the ecosystem it finds — so the self-correction loop works out of
// the box, not only for projects with a hand-written validate: config.
//
// It only emits a stage when the required tool is actually on PATH, so a missing
// toolchain never turns into a spurious validation failure. Detection stops at
// the first ecosystem matched (a repo is assumed to be primarily one thing).
func Detect(dir string) []validate.Stage {
	switch {
	case exists(dir, "go.mod") && have("go"):
		return []validate.Stage{
			{Name: "build", Command: "go build ./..."},
			{Name: "vet", Command: "go vet ./..."},
			{Name: "test", Command: "go test ./..."},
		}

	case exists(dir, "package.json"):
		return nodeStages(dir)

	case exists(dir, "Cargo.toml") && have("cargo"):
		stages := []validate.Stage{{Name: "build", Command: "cargo build --quiet"}}
		if have("cargo-clippy") {
			stages = append(stages, validate.Stage{Name: "lint", Command: "cargo clippy --quiet -- -D warnings"})
		}
		stages = append(stages, validate.Stage{Name: "test", Command: "cargo test --quiet"})
		return stages

	case pythonProject(dir):
		return pythonStages(dir)
	}

	// Generic fallback: standalone JavaScript with no package.json.
	if have("node") && glob(dir, "*.js") {
		return []validate.Stage{{Name: "syntax", Command: jsSyntaxCheck}}
	}
	return nil
}

// nodeStages picks npm scripts that actually exist (build/lint/test).
func nodeStages(dir string) []validate.Stage {
	if !have("npm") {
		return nil
	}
	scripts := npmScripts(dir)
	var stages []validate.Stage
	for _, s := range []struct{ name, script string }{
		{"build", "build"}, {"lint", "lint"}, {"test", "test"},
	} {
		if _, ok := scripts[s.script]; ok {
			stages = append(stages, validate.Stage{Name: s.name, Command: "npm run " + s.script + " --silent"})
		}
	}
	return stages
}

// pythonStages compiles all sources, and adds lint/test when tools are present.
func pythonStages(dir string) []validate.Stage {
	py := pythonBin()
	if py == "" {
		return nil
	}
	stages := []validate.Stage{{Name: "compile", Command: py + " -m compileall -q ."}}
	if have("ruff") {
		stages = append(stages, validate.Stage{Name: "lint", Command: "ruff check ."})
	}
	if have("pytest") {
		stages = append(stages, validate.Stage{Name: "test", Command: "pytest -q"})
	}
	return stages
}

// jsSyntaxCheck runs `node --check` over every tracked/visible .js file.
const jsSyntaxCheck = `set -e; files=$(git ls-files '*.js' 2>/dev/null); ` +
	`[ -z "$files" ] && files=$(find . -name '*.js' -not -path './node_modules/*'); ` +
	`for f in $files; do node --check "$f"; done`

func pythonProject(dir string) bool {
	return exists(dir, "pyproject.toml") || exists(dir, "setup.py") ||
		exists(dir, "requirements.txt") || glob(dir, "*.py")
}

func pythonBin() string {
	for _, b := range []string{"python3", "python"} {
		if have(b) {
			return b
		}
	}
	return ""
}

func npmScripts(dir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return nil
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(data, &pkg) != nil {
		return nil
	}
	return pkg.Scripts
}

func exists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func glob(dir, pattern string) bool {
	m, _ := filepath.Glob(filepath.Join(dir, pattern))
	return len(m) > 0
}

func have(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
