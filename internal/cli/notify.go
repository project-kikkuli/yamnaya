package cli

import "os/exec"

// desktopNotify raises a macOS notification through terminal-notifier when it is installed, else through osascript.
func desktopNotify(title, body string) error {
	if bin, err := exec.LookPath("terminal-notifier"); err == nil {
		return exec.Command(bin, "-title", title, "-message", body, "-group", "ym").Run()
	}
	return exec.Command("osascript", "-e", "on run argv", "-e", "display notification (item 2 of argv) with title (item 1 of argv)", "-e", "end run", title, body).Run()
}
