//go:build unix

package runner

import (
	"os/exec"
	"syscall"
)

// killTree makes cancelling cmd kill its whole process group, not just the
// direct child: agent CLIs are often wrappers (sh, node) whose children would
// otherwise keep running — and keep editing files — after a cancel.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
