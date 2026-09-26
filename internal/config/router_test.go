package config

import (
	"testing"

	"github.com/crossben/orchestra-code/internal/router"
)

const classifierYAML = `
router:
  classifier:
    provider: anthropic
    model: claude-haiku-4-5
    api_key_env: ORCH_TEST_CLASSIFIER_KEY
`

func TestLoadMergesRouterClassifier(t *testing.T) {
	cfg, err := parseForTest(t, classifierYAML)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Router.Classifier
	if got.Provider != "anthropic" || got.Model != "claude-haiku-4-5" || got.APIKeyEnv != "ORCH_TEST_CLASSIFIER_KEY" {
		t.Fatalf("classifier not merged: %+v", got)
	}
	// Other router defaults survive the partial override.
	if cfg.Router.Agent != "claude" || cfg.Router.Routes["implement"] != "opencode" {
		t.Fatalf("router defaults lost: %+v", cfg.Router)
	}
}

func TestBuildClassifierFallsBackWithoutKey(t *testing.T) {
	t.Setenv("ORCH_TEST_CLASSIFIER_KEY", "")
	cfg, err := parseForTest(t, classifierYAML)
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.BuildRouter(cfg.BuildRegistry())
	if err != nil {
		t.Fatalf("missing key must not fail startup: %v", err)
	}
	if _, ok := r.Classifier().(*router.CLIClassifier); !ok {
		t.Fatalf("want CLI classifier fallback, got %T", r.Classifier())
	}
}

func TestBuildClassifierUsesAPIWhenKeySet(t *testing.T) {
	t.Setenv("ORCH_TEST_CLASSIFIER_KEY", "sk-test")
	cfg, err := parseForTest(t, classifierYAML)
	if err != nil {
		t.Fatal(err)
	}
	r, err := cfg.BuildRouter(cfg.BuildRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Classifier().(*router.APIClassifier); !ok {
		t.Fatalf("want API classifier, got %T", r.Classifier())
	}
}

func TestBuildClassifierFallsBackOnBadProvider(t *testing.T) {
	t.Setenv("ORCH_TEST_CLASSIFIER_KEY", "sk-test")
	cfg := Default()
	cfg.Router.Classifier = ClassifierConfig{Provider: "nosuch", Model: "m", APIKeyEnv: "ORCH_TEST_CLASSIFIER_KEY"}
	r, err := cfg.BuildRouter(cfg.BuildRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Classifier().(*router.CLIClassifier); !ok {
		t.Fatalf("want CLI classifier fallback, got %T", r.Classifier())
	}
}

func TestBuildClassifierDefaultIsCLI(t *testing.T) {
	cfg := Default()
	r, err := cfg.BuildRouter(cfg.BuildRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Classifier().(*router.CLIClassifier); !ok {
		t.Fatalf("default must stay the CLI classifier, got %T", r.Classifier())
	}
}
