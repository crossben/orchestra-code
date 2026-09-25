package ui

import (
	"os/exec"
	"runtime"
)

// Notify sends a desktop notification. Best-effort — never returns an error
// because notifications are cosmetic. Uses notify-send (Linux), osascript
// (macOS), or is a no-op on unsupported platforms.
func Notify(title, body string) {
	if !Enabled {
		return
	}
	switch runtime.GOOS {
	case "linux":
		cmd := exec.Command("notify-send", "--urgency=low", title, body)
		_ = cmd.Run()
	case "darwin":
		script := `display notification %q with title %q`
		cmd := exec.Command("osascript", "-e", script, body, title)
		_ = cmd.Run()
	}
}
