//go:build !unix

package runner

import "os/exec"

// killTree is a no-op where process groups are unavailable; WaitDelay still
// stops a cancelled run from hanging on inherited pipes.
func killTree(*exec.Cmd) {}
