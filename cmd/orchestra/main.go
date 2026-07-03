// Command orchestra is the CLI entrypoint.
//
// M1: multiple agents behind a common interface, a YAML config, and an
// interactive chat shell (the default when run with no subcommand). Bare
// `orchestra` drops you into the shell; `orchestra run` is the scriptable
// one-shot entrypoint. Both drive the same supervised engine.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	// Signal-aware context so Ctrl-C / SIGTERM cancels the running agent and
	// lets deferred cleanup (e.g. worktree teardown) run instead of leaking.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "orchestra:", err)
		os.Exit(1)
	}
}
