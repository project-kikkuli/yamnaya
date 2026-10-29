// Package clipboard puts text on the owner's clipboard from a terminal, local or over SSH.
package clipboard

import (
	"encoding/base64"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// Way is how text reaches the clipboard.
type Way int

const (
	// Pbcopy runs macOS's pbcopy, for a session on the owner's own Mac.
	Pbcopy Way = iota
	// OSC52 writes the OSC 52 escape to the terminal, which sets the clipboard of the machine the terminal runs on.
	OSC52
)

func (w Way) String() string {
	if w == Pbcopy {
		return "pbcopy"
	}
	return "the terminal (OSC 52)"
}

// Env is what Pick looks at.
type Env struct {
	GOOS     string
	Getenv   func(string) string
	LookPath func(string) (string, error)
}

// Here is this process's Env.
func Here() Env { return Env{GOOS: runtime.GOOS, Getenv: os.Getenv, LookPath: exec.LookPath} }

// Pick is pbcopy on a Mac reached directly, and OSC 52 over SSH, where pbcopy would fill the remote host's clipboard, or without pbcopy.
func Pick(e Env) Way {
	if e.Getenv("SSH_TTY") != "" || e.Getenv("SSH_CONNECTION") != "" || e.GOOS != "darwin" {
		return OSC52
	}
	if _, err := e.LookPath("pbcopy"); err != nil {
		return OSC52
	}
	return Pbcopy
}

// Sequence is the OSC 52 escape that sets the clipboard to text.
func Sequence(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}

// Clean is text as it should paste: no trailing spaces on a line, no carriage returns, one final newline.
func Clean(text string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n") + "\n"
}

// Copy puts text on the clipboard the way Pick chooses, writing OSC 52 to the controlling terminal.
func Copy(text string) (Way, error) {
	way := Pick(Here())
	if way == Pbcopy {
		cmd := exec.Command("pbcopy")
		cmd.Stdin = strings.NewReader(text)
		return way, cmd.Run()
	}
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return way, err
	}
	defer tty.Close()
	_, err = tty.WriteString(Sequence(text))
	return way, err
}
