// Package terminal finds, focuses and types into macOS Terminal tabs by tty.
package terminal

import (
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/osa"
)

const Name = "terminal"

type Host struct {
	Run     osa.Runner
	Running func() bool

	mu   sync.Mutex
	ttys map[string]core.Handle
	at   time.Time
}

func New() *Host {
	return &Host{Run: osa.Run, Running: func() bool { return osa.Running("Terminal") }}
}

func (h *Host) Name() string { return Name }

const listScript = `set out to ""
tell application "Terminal"
	repeat with w in windows
		set n to 0
		repeat with t in tabs of w
			set n to n + 1
			set out to out & (tty of t) & tab & (id of w) & tab & n & linefeed
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
		if len(f) == 3 {
			h.ttys[f[0]] = core.Handle{Host: Name, ID: f[0], Label: "window " + f[1] + " tab " + f[2]}
		}
	}
	return h.ttys
}

func (h *Host) Locate(tty string) (core.Handle, bool) {
	hd, ok := h.list()[tty]
	return hd, ok
}

// find binds t to the tab whose tty is item 1 of argv, then runs body.
func find(body string) string {
	return `on run argv
	tell application "Terminal"
		repeat with w in windows
			repeat with t in tabs of w
				if tty of t is (item 1 of argv) then
					` + body + `
					return "ok"
				end if
			end repeat
		end repeat
	end tell
	error "tab not found"
end run`
}

var (
	focusScript = find(`set selected of t to true
					set index of w to 1
					activate`)
	typeScript   = find(`do script (item 2 of argv) in t`)
	screenScript = find(`return contents of t`)
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
