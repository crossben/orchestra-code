package validate

import (
	"context"
	"strings"
	"testing"
)

func TestRunPipelineObservedStopsAtFirstFailure(t *testing.T) {
	var seen []string
	rep := RunPipelineObserved(context.Background(), t.TempDir(), []Stage{
		{Name: "build", Command: "true"},
		{Name: "lint", Command: "echo nope; exit 1"},
		{Name: "test", Command: "true"},
	}, func(done bool, r StageResult) {
		switch {
		case !done:
			seen = append(seen, "start:"+r.Name)
		case r.Passed:
			seen = append(seen, "pass:"+r.Name)
		default:
			seen = append(seen, "fail:"+r.Name)
		}
	})
	if got, want := strings.Join(seen, " "), "start:build pass:build start:lint fail:lint"; got != want {
		t.Fatalf("observer saw %q, want %q", got, want)
	}
	if rep.Passed() || len(rep.Stages) != 2 {
		t.Fatalf("report should fail after 2 stages: %+v", rep)
	}
	if !strings.Contains(rep.FeedbackText(), "nope") {
		t.Fatalf("feedback missing stage output: %q", rep.FeedbackText())
	}
}

func TestRunPipelineEmptyIsSkipped(t *testing.T) {
	if rep := RunPipeline(context.Background(), t.TempDir(), nil); !rep.Skipped || rep.Passed() {
		t.Fatalf("empty pipeline should be skipped, not passed: %+v", rep)
	}
}
