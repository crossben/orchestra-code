package runner

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
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

// Cancelling must stop the agent's children too, and return promptly even
// though a grandchild inherited the output pipe.
func TestRunProbeCancelKillsChildren(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(200 * time.Millisecond); cancel() }()
	start := time.Now()
	_, _, err := RunProbe(ctx, Spec{Bin: "sh", Args: []string{"-c", "sleep 30; echo done"}, Dir: t.TempDir()})
	if err != context.Canceled {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("cancel took %s", d)
	}
}

// An agent that exits 0 but leaves a background child holding the output
// pipe must still count as a clean, prompt exit.
func TestRunProbeLingeringChildIsCleanExit(t *testing.T) {
	start := time.Now()
	_, res, err := RunProbe(context.Background(), Spec{Bin: "sh", Args: []string{"-c", "sleep 30 & echo started"}, Dir: t.TempDir()})
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("want clean exit, got code=%d err=%v", res.ExitCode, err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("waited %s on a lingering child", d)
	}
}
