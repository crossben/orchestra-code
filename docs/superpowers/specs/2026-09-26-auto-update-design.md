# Self-updater — design

Date: 2026-09-26 · Scope: new `internal/update` package, a launch hook and an `orchestra update`
subcommand in `cmd/orchestra`. No change to the engine, agents or the supervised loop.

## Intent

Users who installed a release archive should find out about new releases without watching GitHub, and
be able to upgrade in place with one keypress. After an upgrade the old process exits so the user never
keeps working in a binary that no longer matches what is on disk.

Non-goals: background/silent updates, updating Homebrew/Scoop installs through their package manager,
signature verification beyond the published `checksums.txt`, elevating privileges (no sudo).

## Constraints

- Stdlib only (`net/http`, `archive/tar`, `archive/zip`, `compress/gzip`, `crypto/sha256`). CGO-free.
- Never breaks non-interactive use: no network, no prompt, no output unless stdin **and** stdout are
  terminals. Tests stay offline and deterministic (`httptest`, temp dirs).
- Relies on the GoReleaser contract: archive `orchestra_<Version>_<GOOS>_<GOARCH>.tar.gz` (`.zip` on
  windows) containing `orchestra`/`orchestra.exe`, plus a `checksums.txt` in `<sha256>  <file>` format.

## `internal/update`

- `Check(ctx, current) (Release, bool, error)` — GET `<api>/repos/crossben/orchestra-code/releases/latest`
  (base URL injectable), parse `tag_name`, `prerelease`, `assets[].{name,browser_download_url,size}`.
  Returns `newer=true` only when the latest release is strictly greater by semver.
  - `Checkable(current)` is false for `dev`, unparseable versions and GoReleaser snapshots (`-next`);
    `Check` then returns immediately without touching the network.
  - A prerelease (tag suffix or GitHub `prerelease` flag) is never offered over a stable build. A
    prerelease build may be offered the matching stable (`0.9.0-rc1` → `0.9.0`) or a newer prerelease.
- `Install(ctx, rel, exePath)`:
  1. Pick the asset for `runtime.GOOS/GOARCH`; error if the release has none for this platform.
  2. Download `checksums.txt` (1 MiB cap) and the archive (256 MiB cap).
  3. Verify SHA-256 — refuse on mismatch or when the archive has no entry.
  4. Extract only the `orchestra`/`orchestra.exe` regular-file entry (at the root or one directory
     deep). Any absolute or `..` entry makes the whole archive unsafe → error.
  5. Resolve symlinks of `exePath`; write a temp file next to it, `chmod 0755`, then replace:
     unix: `os.Rename` over the exe. windows: rename the running exe to `<exe>.old`, move the new one in
     (roll back on failure); `CleanupOld` removes a stale `.old` on the next launch.
  6. A non-writable directory yields a clear error suggesting re-running with sufficient permissions or
     reinstalling via the original method.
- Throttle state in `~/.orchestra/update.json`: `{last_check, latest}`. `Due(state, now)` is true after
  24h. The launch hook records the attempt time *before* the network call, so a slow or offline network
  costs at most one 2s wait per day.

## Launch hook (`PersistentPreRunE` on root)

Skipped (silently, no network) when any holds:

- stdin or stdout is not a terminal (pipes, scripts, tests, TUI-in-CI);
- `CI` is set; `ORCHESTRA_NO_UPDATE_CHECK=1`;
- the version is not checkable (`dev`, unparseable, `-next` snapshot);
- the command is `help`, `completion` (and its children), cobra's `__complete*`, or `update`;
- `--version`/`--help` (cobra handles these before pre-run anyway);
- the last check was less than 24h ago.

Flow: 2s timeout on the check; errors are silent (printed to stderr only with `ORCHESTRA_DEBUG=1`). If
newer: `Orchestra vX is available (you have vY). Download and install now? [Y/n]`.

- Yes (Enter/y/yes): install behind a TTY-aware spinner (`internal/ui`), print
  `Updated to vX. Please relaunch orchestra.` and `os.Exit(0)` — the old process does not continue.
- No: continue with the requested command. The throttle means no re-prompt for 24h.
- Install failure: print the error, continue running the current version.

Stdin is read one byte at a time up to the newline so the prompt never swallows input meant for the
shell that starts afterwards.

## `orchestra update`

Forces a check (ignores the throttle, still records it). `--check` only reports; `-y/--yes` skips the
prompt. Same install/exit behaviour as the hook. On a dev/snapshot build it explains that updates are
only offered for release builds.

## Tests

Version comparison table; `Checkable`; `Check` parsing, non-200, prerelease handling, dev short-circuit
(server must not be hit); throttle round-trip with a temp state path; `Install` happy path with an
in-test tar.gz and zip + checksums; checksum mismatch; missing checksum entry; missing platform asset;
traversal entry; replacement of a temp "exe" (unix rename and windows `.old` strategy); hook skip
conditions (non-TTY, CI, env var, dev version, skipped commands); prompt answer parsing.
