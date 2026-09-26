package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crossben/orchestra-code/internal/update"
	"github.com/spf13/cobra"
)

func ttyEnv(vars map[string]string) updateEnv {
	return updateEnv{stdinTTY: true, stdoutTTY: true, getenv: func(k string) string { return vars[k] }}
}

// findCmd resolves a command path on a fresh root, like cobra would.
func findCmd(t *testing.T, args ...string) *cobra.Command {
	t.Helper()
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	cmd, _, err := root.Find(args)
	if err != nil {
		t.Fatalf("find %v: %v", args, err)
	}
	return cmd
}

func TestShouldAutoCheck(t *testing.T) {
	run := findCmd(t, "run")
	cases := []struct {
		name    string
		cmd     *cobra.Command
		version string
		env     updateEnv
		want    bool
	}{
		{"interactive release build", run, "0.8.0", ttyEnv(nil), true},
		{"root command", findCmd(t), "0.8.0", ttyEnv(nil), true},
		{"stdin piped", run, "0.8.0", updateEnv{stdinTTY: false, stdoutTTY: true, getenv: ttyEnv(nil).getenv}, false},
		{"stdout piped", run, "0.8.0", updateEnv{stdinTTY: true, stdoutTTY: false, getenv: ttyEnv(nil).getenv}, false},
		{"CI", run, "0.8.0", ttyEnv(map[string]string{"CI": "true"}), false},
		{"opt-out", run, "0.8.0", ttyEnv(map[string]string{"ORCHESTRA_NO_UPDATE_CHECK": "1"}), false},
		{"opt-out explicitly off", run, "0.8.0", ttyEnv(map[string]string{"ORCHESTRA_NO_UPDATE_CHECK": "0"}), true},
		{"dev build", run, "dev", ttyEnv(nil), false},
		{"snapshot build", run, "0.8.1-next", ttyEnv(nil), false},
		{"unparseable", run, "abc123", ttyEnv(nil), false},
		{"update command", findCmd(t, "update"), "0.8.0", ttyEnv(nil), false},
		{"help command", findCmd(t, "help"), "0.8.0", ttyEnv(nil), false},
		{"completion", findCmd(t, "completion", "bash"), "0.8.0", ttyEnv(nil), false},
	}
	for _, c := range cases {
		if got := shouldAutoCheck(c.cmd, c.version, c.env); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}

	// --version / --help flags.
	root := newRootCmd()
	root.InitDefaultVersionFlag()
	_ = root.ParseFlags([]string{"--version"})
	if shouldAutoCheck(root, "0.8.0", ttyEnv(nil)) {
		t.Error("--version should skip the check")
	}
}

func TestConfirmAndReadLine(t *testing.T) {
	for in, want := range map[string]bool{"\n": true, "y\n": true, "YES\n": true, " y \r\n": true, "n\n": false, "no\n": false, "": false, "nope": false} {
		if got := confirmDefaultYes(readLine(strings.NewReader(in))); got != want {
			t.Errorf("answer %q: got %v, want %v", in, got, want)
		}
	}
	// readLine must not consume past the newline.
	r := strings.NewReader("y\nnext command\n")
	readLine(r)
	if rest := readLine(r); rest != "next command" {
		t.Errorf("remaining input = %q", rest)
	}
}

// fakeReleases serves a GitHub-like API plus the release assets.
type fakeReleases struct {
	srv      *httptest.Server
	apiHits  int32
	fail     atomic.Bool
	latestV  string
	archive  []byte
	checksum string
}

func newFakeReleases(t *testing.T, latest string) *fakeReleases {
	t.Helper()
	f := &fakeReleases{latestV: latest}
	binName := "orchestra"
	if runtime.GOOS == "windows" {
		binName = "orchestra.exe"
	}
	name := update.AssetName(latest, runtime.GOOS, runtime.GOARCH)
	if strings.HasSuffix(name, ".zip") {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create(binName)
		_, _ = w.Write([]byte("NEW-" + latest))
		_ = zw.Close()
		f.archive = buf.Bytes()
	} else {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		body := []byte("NEW-" + latest)
		_ = tw.WriteHeader(&tar.Header{Name: binName, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(body)
		_ = tw.Close()
		_ = gz.Close()
		f.archive = buf.Bytes()
	}
	sum := sha256.Sum256(f.archive)
	f.checksum = hex.EncodeToString(sum[:]) + "  " + name + "\n"

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/"+update.DefaultRepo+"/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&f.apiHits, 1)
		if f.fail.Load() {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"tag_name":"v%s","assets":[{"name":%q,"browser_download_url":"%s/dl/a"},{"name":"checksums.txt","browser_download_url":"%s/dl/sums"}]}`,
			f.latestV, name, f.srv.URL, f.srv.URL)
	})
	mux.HandleFunc("/dl/a", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(f.archive) })
	mux.HandleFunc("/dl/sums", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(f.checksum)) })
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func testUpdater(t *testing.T, f *fakeReleases, current, answer string) (*updater, *bytes.Buffer) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "orchestra")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	out := &bytes.Buffer{}
	return &updater{
		client:    &update.Client{APIBase: f.srv.URL},
		statePath: filepath.Join(t.TempDir(), "update.json"),
		current:   current,
		exePath:   exe,
		in:        strings.NewReader(answer),
		out:       out,
		now:       func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) },
	}, out
}

func exeContent(t *testing.T, u *updater) string {
	t.Helper()
	b, err := os.ReadFile(u.exePath)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestLaunchCheckInstallsAndExits(t *testing.T) {
	f := newFakeReleases(t, "0.9.0")
	u, out := testUpdater(t, f, "0.8.0", "\n")
	exited := -1
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u, func(code int) { exited = code })
	if exited != 0 {
		t.Fatalf("exit code = %d, want 0 (output: %s)", exited, out)
	}
	if got := exeContent(t, u); got != "NEW-0.9.0" {
		t.Fatalf("exe = %q", got)
	}
	s := out.String()
	if !strings.Contains(s, "Orchestra v0.9.0 is available (you have v0.8.0). Download and install now? [Y/n]") ||
		!strings.Contains(s, "Updated to v0.9.0. Please relaunch orchestra.") {
		t.Fatalf("output:\n%s", s)
	}
}

func TestLaunchCheckDeclineThenThrottled(t *testing.T) {
	f := newFakeReleases(t, "0.9.0")
	u, out := testUpdater(t, f, "0.8.0", "n\n")
	exit := func(int) { t.Fatal("must not exit after declining") }
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u, exit)
	if exeContent(t, u) != "OLD" || !strings.Contains(out.String(), "Skipped") {
		t.Fatalf("declined flow wrong; output:\n%s", out)
	}
	st := update.LoadState(u.statePath)
	if st.Latest != "0.9.0" || st.LastCheck.IsZero() {
		t.Fatalf("state not recorded: %+v", st)
	}

	// Same day: no network, no prompt.
	out.Reset()
	u.in = strings.NewReader("\n")
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u, exit)
	if atomic.LoadInt32(&f.apiHits) != 1 || out.Len() != 0 {
		t.Fatalf("throttle failed: hits=%d output=%q", f.apiHits, out)
	}

	// A day later it asks again.
	u.now = func() time.Time { return time.Date(2026, 9, 27, 12, 0, 1, 0, time.UTC) }
	u.in = strings.NewReader("no\n")
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u, exit)
	if atomic.LoadInt32(&f.apiHits) != 2 || !strings.Contains(out.String(), "is available") {
		t.Fatalf("expected a new prompt after 24h: hits=%d output=%q", f.apiHits, out)
	}
}

func TestLaunchCheckSilentOnErrorAndSkips(t *testing.T) {
	f := newFakeReleases(t, "0.9.0")
	f.fail.Store(true)
	u, out := testUpdater(t, f, "0.8.0", "\n")
	exit := func(int) { t.Fatal("must not exit") }
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u, exit)
	if out.Len() != 0 || exeContent(t, u) != "OLD" {
		t.Fatalf("network errors must be silent; output=%q", out)
	}

	// Skip conditions never reach the network.
	f.fail.Store(false)
	hits := atomic.LoadInt32(&f.apiHits)
	for _, env := range []updateEnv{
		{stdinTTY: false, stdoutTTY: true, getenv: ttyEnv(nil).getenv},
		ttyEnv(map[string]string{"CI": "1"}),
		ttyEnv(map[string]string{"ORCHESTRA_NO_UPDATE_CHECK": "1"}),
	} {
		u2, out2 := testUpdater(t, f, "0.8.0", "\n")
		launchCheck(findCmd(t, "run"), env, u2, exit)
		if out2.Len() != 0 {
			t.Errorf("unexpected output %q", out2)
		}
	}
	u3, _ := testUpdater(t, f, "dev", "\n")
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u3, exit)
	if atomic.LoadInt32(&f.apiHits) != hits {
		t.Fatalf("skip conditions hit the network %d times", f.apiHits-hits)
	}
}

func TestLaunchCheckInstallFailureContinues(t *testing.T) {
	f := newFakeReleases(t, "0.9.0")
	f.checksum = strings.Repeat("0", 64) + "  " + update.AssetName("0.9.0", runtime.GOOS, runtime.GOARCH) + "\n"
	u, out := testUpdater(t, f, "0.8.0", "y\n")
	launchCheck(findCmd(t, "run"), ttyEnv(nil), u, func(int) { t.Fatal("must not exit on failed install") })
	if exeContent(t, u) != "OLD" || !strings.Contains(out.String(), "update failed") {
		t.Fatalf("output:\n%s", out)
	}
}

func TestRunUpdateCommand(t *testing.T) {
	f := newFakeReleases(t, "0.9.0")

	// --check only reports, even though the throttle would say "not due".
	u, out := testUpdater(t, f, "0.8.0", "")
	_ = update.SaveState(u.statePath, update.State{LastCheck: u.now()})
	if err := runUpdate(context.Background(), u, true, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "v0.9.0 is available") || exeContent(t, u) != "OLD" || atomic.LoadInt32(&f.apiHits) != 1 {
		t.Fatalf("--check: hits=%d output=%q", f.apiHits, out)
	}

	// -y installs without a prompt.
	u, out = testUpdater(t, f, "0.8.0", "")
	if err := runUpdate(context.Background(), u, false, true); err != nil {
		t.Fatal(err)
	}
	if exeContent(t, u) != "NEW-0.9.0" || strings.Contains(out.String(), "[Y/n]") {
		t.Fatalf("-y: output=%q", out)
	}

	// Up to date.
	u, out = testUpdater(t, f, "0.9.0", "")
	if err := runUpdate(context.Background(), u, false, false); err != nil || !strings.Contains(out.String(), "up to date") {
		t.Fatalf("up to date: err=%v output=%q", err, out)
	}

	// Dev build explains itself without touching the network.
	hits := atomic.LoadInt32(&f.apiHits)
	u, out = testUpdater(t, f, "dev", "")
	if err := runUpdate(context.Background(), u, false, false); err != nil || !strings.Contains(out.String(), "development build") || atomic.LoadInt32(&f.apiHits) != hits {
		t.Fatalf("dev: err=%v output=%q", err, out)
	}

	// Check failure is an error for the explicit command.
	f.fail.Store(true)
	u, _ = testUpdater(t, f, "0.8.0", "")
	if err := runUpdate(context.Background(), u, false, false); err == nil {
		t.Fatal("want error when the check fails")
	}
}
