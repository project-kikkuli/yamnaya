// Package osa runs AppleScript for the macOS terminal hosts.
package osa

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a script with its arguments and returns stdout.
type Runner func(script string, args ...string) (string, error)

func Run(script string, args ...string) (string, error) {
	cmd := exec.Command("osascript", append([]string{"-e", script}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// Running reports whether a process with this exact name runs, so a host never launches its app by asking it something.
func Running(process string) bool {
	return exec.Command("pgrep", "-xq", process).Run() == nil
}
