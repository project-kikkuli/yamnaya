// Package app brings forward the macOS terminal app window and tab that shows a tty: Terminal.app and iTerm2.
package app

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Tab is one terminal an app shows: its window id, its tab's position from 1, and iTerm2's session id within the tab.
type Tab struct {
	App, Window, Tab, Session, TTY string
}

// Apps reads and selects terminal app tabs through AppleScript.
type Apps struct {
	// Script runs an AppleScript program with arguments and returns what it printed.
	Script func(program string, args ...string) (string, error)
}

func New() *Apps { return &Apps{Script: osascript} }

func osascript(program string, args ...string) (string, error) {
	cmd := exec.Command("osascript", append([]string{"-"}, args...)...)
	cmd.Stdin = strings.NewReader(program)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("osascript: %w: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("osascript: %w", err)
	}
	return string(out), nil
}

type app struct {
	name, list, pick string
}

var apps = []app{
	{name: "Terminal", list: `on run
	set out to ""
	set sep to character id 9
	if application "Terminal" is running then
		tell application "Terminal"
			repeat with w in windows
				set i to 0
				repeat with t in tabs of w
					set i to i + 1
					set out to out & (id of w as text) & sep & (i as text) & sep & sep & (tty of t) & linefeed
				end repeat
			end repeat
		end tell
	end if
	return out
end run`, pick: `on run argv
	tell application "Terminal"
		set w to window id ((item 1 of argv) as integer)
		set selected of tab ((item 2 of argv) as integer) of w to true
		set index of w to 1
		activate
	end tell
end run`},
	{name: "iTerm2", list: `on run
	set out to ""
	set sep to character id 9
	if application "iTerm2" is running then
		tell application "iTerm2"
			repeat with w in windows
				set i to 0
				repeat with t in tabs of w
					set i to i + 1
					repeat with s in sessions of t
						set out to out & (id of w as text) & sep & (i as text) & sep & (id of s) & sep & (tty of s) & linefeed
					end repeat
				end repeat
			end repeat
		end tell
	end if
	return out
end run`, pick: `on run argv
	tell application "iTerm2"
		set w to window id ((item 1 of argv) as integer)
		set n to (item 2 of argv) as integer
		set t to item n of tabs of w
		select w
		tell t to select
		repeat with s in sessions of t
			if id of s is (item 3 of argv) then tell s to select
		end repeat
		activate
	end tell
end run`},
}

// Tabs lists every tab of the terminal apps that run; errs says which apps could not be read.
func (a *Apps) Tabs() (tabs []Tab, errs error) {
	var failed []error
	for _, ap := range apps {
		out, err := a.Script(ap.list)
		if err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", ap.name, err))
			continue
		}
		for _, line := range strings.Split(out, "\n") {
			f := strings.Split(strings.TrimRight(line, "\r"), "\t")
			if len(f) == 4 && f[3] != "" {
				tabs = append(tabs, Tab{App: ap.name, Window: f[0], Tab: f[1], Session: f[2], TTY: f[3]})
			}
		}
	}
	return tabs, errors.Join(failed...)
}

// Front selects the window and tab showing tty and brings its app forward; ErrNoHost when no app shows it, an error when an app
// that might could not be read.
func (a *Apps) Front(tty string) error {
	if runtime.GOOS != "darwin" {
		return core.ErrNoHost
	}
	tabs, errs := a.Tabs()
	for _, t := range tabs {
		if t.TTY != tty {
			continue
		}
		for _, ap := range apps {
			if ap.name == t.App {
				_, err := a.Script(ap.pick, t.Window, t.Tab, t.Session)
				return err
			}
		}
	}
	if errs != nil {
		return fmt.Errorf("can't read the terminal apps for %s: %w", strings.TrimPrefix(tty, "/dev/"), errs)
	}
	return core.ErrNoHost
}
