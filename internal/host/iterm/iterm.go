// Package iterm finds, focuses and types into iTerm2 sessions by tty.
package iterm

import (
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/osa"
)

const Name = "iterm"

type Host struct {
	Run     osa.Runner
	Running func() bool

	mu   sync.Mutex
	ttys map[string]core.Handle
	at   time.Time
}

func New() *Host {
	return &Host{Run: osa.Run, Running: func() bool { return osa.Running("iTerm2") }}
}

func (h *Host) Name() string { return Name }

const listScript = `set out to ""
tell application "iTerm"
	repeat with w in windows
		set n to 0
		repeat with t in tabs of w
			set n to n + 1
			repeat with s in sessions of t
				set out to out & (tty of s) & tab & (id of s) & tab & (id of w) & tab & n & linefeed
			end repeat
		end repeat
	end repeat
end tell
return out`

func (h *Host) list() map[string]core.Handle {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ttys != nil && time.Since(h.at) < time.Second {
		return h.ttys
	}
	h.ttys, h.at = map[string]core.Handle{}, time.Now()
	if !h.Running() {
		return h.ttys
	}
	out, err := h.Run(listScript)
	if err != nil {
		return h.ttys
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(strings.TrimSpace(line), "\t")
		if len(f) == 4 {
			h.ttys[f[0]] = core.Handle{Host: Name, ID: f[1], Label: "window " + f[2] + " tab " + f[3]}
		}
	}
	return h.ttys
}

func (h *Host) Locate(tty string) (core.Handle, bool) {
	hd, ok := h.list()[tty]
	return hd, ok
}

// find binds s to the session with id (item 1 of argv), then runs body.
func find(body string) string {
	return `on run argv
	tell application "iTerm"
		repeat with w in windows
			repeat with t in tabs of w
				repeat with s in sessions of t
					if id of s is (item 1 of argv) then
						` + body + `
						return "ok"
					end if
				end repeat
			end repeat
		end repeat
	end tell
	error "session not found"
end run`
}

var (
	focusScript = find(`select w
						tell t to select
						tell s to select
						activate`)
	typeScript = find(`tell s to write text (item 2 of argv) newline no
						delay 0.15
						tell s to write text ""`)
	screenScript = find(`return contents of s`)
)

func (h *Host) Focus(hd core.Handle) (string, error) {
	_, err := h.Run(focusScript, hd.ID)
	return "", err
}

func (h *Host) Type(hd core.Handle, text string) error {
	_, err := h.Run(typeScript, hd.ID, text)
	return err
}

func (h *Host) Screen(hd core.Handle) (string, error) {
	return h.Run(screenScript, hd.ID)
}
