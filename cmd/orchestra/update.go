package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/crossben/orchestra-code/internal/ui"
	"github.com/crossben/orchestra-code/internal/update"
	"github.com/spf13/cobra"
)

// autoCheckTimeout bounds the launch-time release check so a slow network
// never delays startup noticeably.
const autoCheckTimeout = 2 * time.Second

// installTimeout bounds the download + replace once the user said yes.
const installTimeout = 5 * time.Minute

// updateEnv is everything the launch hook's skip decision depends on, so it
// can be tested without a terminal.
type updateEnv struct {
	stdinTTY  bool
	stdoutTTY bool
	getenv    func(string) string
}

func realUpdateEnv() updateEnv {
	return updateEnv{stdinTTY: isTTY(os.Stdin), stdoutTTY: isTTY(os.Stdout), getenv: os.Getenv}
}

func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// skipUpdateCommands never trigger the launch check.
var skipUpdateCommands = map[string]bool{
	"help":                          true,
	"completion":                    true,
	"update":                        true,
	cobra.ShellCompRequestCmd:       true, // __complete
	cobra.ShellCompNoDescRequestCmd: true, // __completeNoDesc
}

// shouldAutoCheck decides whether the launch hook may touch the network.
// Anything non-interactive (pipes, scripts, CI, tests) is skipped.
func shouldAutoCheck(cmd *cobra.Command, current string, env updateEnv) bool {
	if !env.stdinTTY || !env.stdoutTTY {
		return false
	}
	if env.getenv("CI") != "" {
		return false
	}
	if v := env.getenv("ORCHESTRA_NO_UPDATE_CHECK"); v != "" && v != "0" && !strings.EqualFold(v, "false") {
		return false
	}
	if !update.Checkable(current) {
		return false
	}
	for c := cmd; c != nil; c = c.Parent() {
		if skipUpdateCommands[c.Name()] {
			return false
		}
	}
	for _, f := range []string{"version", "help"} {
		if fl := cmd.Flags().Lookup(f); fl != nil && fl.Changed {
			return false
		}
	}
	return true
}

// updater runs the check → prompt → install flow for both the launch hook and
// `orchestra update`. All I/O is injectable for tests.
type updater struct {
	client    *update.Client
	statePath string // "" disables throttle persistence
	current   string
	exePath   string
	in        io.Reader
	out       io.Writer
	now       func() time.Time
}

func newUpdater() *updater {
	exe, _ := os.Executable()
	state, _ := update.DefaultStatePath()
	return &updater{
		client:    &update.Client{UserAgent: "orchestra/" + version},
		statePath: state,
		current:   version,
		exePath:   exe,
		in:        os.Stdin,
		out:       os.Stdout,
		now:       time.Now,
	}
}

// outcome of an update offer.
type updateOutcome int

const (
	outcomeDeclined  updateOutcome = iota // newer release, user said no
	outcomeInstalled                      // replaced the binary — caller must exit
	outcomeFailed                         // newer release, install failed
)

// check performs the (optionally throttled) release check and records the
// attempt. It returns the release and whether it is newer.
func (u *updater) check(ctx context.Context, throttled bool) (update.Release, bool, error) {
	var st update.State
	if u.statePath != "" {
		st = update.LoadState(u.statePath)
	}
	if throttled && !st.Due(u.now()) {
		return update.Release{}, false, nil
	}
	// Record the attempt before the network call: an offline or slow network
	// then costs at most one short wait per interval.
	st.LastCheck = u.now()
	rel, newer, err := u.client.Check(ctx, u.current)
	if err == nil {
		st.Latest = rel.Version
	}
	if u.statePath != "" {
		_ = update.SaveState(u.statePath, st)
	}
	return rel, newer, err
}

// offer prompts (unless assumeYes) and installs rel.
func (u *updater) offer(ctx context.Context, rel update.Release, assumeYes bool) updateOutcome {
	if !assumeYes {
		fmt.Fprintf(u.out, "%s Orchestra v%s is available (you have v%s). Download and install now? [Y/n] ",
			ui.Accent("↑"), rel.Version, strings.TrimPrefix(u.current, "v"))
		if !confirmDefaultYes(readLine(u.in)) {
			fmt.Fprintln(u.out, ui.Dim("  Skipped — you'll be reminded again in a day. Run `orchestra update` any time."))
			return outcomeDeclined
		}
	}
	sp := ui.Spin(fmt.Sprintf("Downloading orchestra v%s…", rel.Version))
	err := u.client.Install(ctx, rel, u.exePath)
	sp.Stop()
	if err != nil {
		fmt.Fprintf(u.out, "%s update failed: %v\n", ui.Danger("✗"), err)
		return outcomeFailed
	}
	fmt.Fprintf(u.out, "%s Updated to v%s. Please relaunch orchestra.\n", ui.Success("✓"), rel.Version)
	return outcomeInstalled
}

// launchCheck is the root PersistentPreRunE body. It never returns an error:
// update problems must not block the command the user asked for.
func launchCheck(cmd *cobra.Command, env updateEnv, u *updater, exit func(int)) {
	update.CleanupOld(u.exePath)
	if !shouldAutoCheck(cmd, u.current, env) {
		return
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, autoCheckTimeout)
	rel, newer, err := u.check(ctx, true)
	cancel()
	if err != nil {
		if env.getenv("ORCHESTRA_DEBUG") != "" {
			fmt.Fprintf(os.Stderr, "orchestra: update check failed: %v\n", err)
		}
		return
	}
	if !newer {
		return
	}
	ictx, icancel := context.WithTimeout(parent, installTimeout)
	defer icancel()
	if u.offer(ictx, rel, false) == outcomeInstalled {
		// Do not keep running the old binary.
		exit(0)
	}
}

func newUpdateCmd() *cobra.Command {
	var checkOnly, yes bool
	cmd := &cobra.Command{
		Use:   "update",
		Short: "Check for a newer Orchestra release and install it",
		Long: "Checks GitHub Releases for a newer Orchestra, and (after confirmation) downloads it,\n" +
			"verifies its SHA-256 against checksums.txt and replaces this executable.\n" +
			"Orchestra also checks automatically at most once a day in interactive terminals;\n" +
			"set ORCHESTRA_NO_UPDATE_CHECK=1 to disable that.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runUpdate(cmd.Context(), newUpdater(), checkOnly, yes)
		},
	}
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only report whether an update is available")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "install without asking")
	return cmd
}

// runUpdate is `orchestra update`: a forced (unthrottled) check.
func runUpdate(ctx context.Context, u *updater, checkOnly, yes bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if !update.Checkable(u.current) {
		fmt.Fprintf(u.out, "This is a development build (%s); updates are only offered for release builds.\n", u.current)
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	sp := ui.Spin("Checking for updates…")
	rel, newer, err := u.check(cctx, false)
	sp.Stop()
	cancel()
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}
	if !newer {
		fmt.Fprintf(u.out, "Orchestra v%s is up to date.\n", strings.TrimPrefix(u.current, "v"))
		return nil
	}
	if checkOnly {
		fmt.Fprintf(u.out, "Orchestra v%s is available (you have v%s). Run `orchestra update` to install it.\n",
			rel.Version, strings.TrimPrefix(u.current, "v"))
		return nil
	}
	ictx, icancel := context.WithTimeout(ctx, installTimeout)
	defer icancel()
	switch u.offer(ictx, rel, yes) {
	case outcomeFailed:
		return errors.New("update not installed")
	}
	// Installed: returning ends the process (exit 0), so the old binary does
	// not keep running. Declined: nothing to do.
	return nil
}

// readLine reads up to a newline one byte at a time, so no input meant for
// whatever runs next (e.g. the shell) is buffered away.
func readLine(r io.Reader) string {
	var b strings.Builder
	buf := make([]byte, 1)
	for b.Len() < 256 {
		n, err := r.Read(buf)
		if n == 1 {
			if buf[0] == '\n' {
				break
			}
			b.WriteByte(buf[0])
		}
		if err != nil {
			if b.Len() == 0 {
				return "n" // EOF with no answer: treat as no
			}
			break
		}
	}
	return strings.TrimSpace(b.String())
}

// confirmDefaultYes: Enter, y, yes → true.
func confirmDefaultYes(ans string) bool {
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "", "y", "yes":
		return true
	}
	return false
}
