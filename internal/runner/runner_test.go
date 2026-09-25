package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunProbeTeesToOutput(t *testing.T) {
	var live bytes.Buffer
	out, res, err := RunProbe(context.Background(), Spec{
		Bin:    "sh",
		Args:   []string{"-c", "echo to-stdout; echo to-stderr 1>&2; exit 3"},
		Dir:    t.TempDir(),
		Output: &live,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 3 {
		t.Fatalf("exit code = %d, want 3", res.ExitCode)
	}
	for _, s := range []string{out, live.String()} {
		if !strings.Contains(s, "to-stdout") || !strings.Contains(s, "to-stderr") {
			t.Fatalf("combined output missing a stream: %q", s)
		}
	}
}
