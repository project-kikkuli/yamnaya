package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// hostNames are every host the board reads or ym can launch on.
func (m Model) hostNames() []string {
	var hosts []string
	for _, r := range m.board.Remotes {
		hosts = append(hosts, r.Host)
	}
	if m.core != nil && m.core.Remotes != nil {
		for _, h := range m.core.Remotes.Hosts() {
			if !slices.Contains(hosts, h) {
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

// hostTag marks a row from another host with that host's nickname; this host's rows carry none.
func (m Model) hostTag(host string) string {
	if host == "" {
		return ""
	}
	if n := core.Nicknames(m.hostNames(), m.nicks)[host]; n != "" {
		return "@" + n
	}
	return "@" + host
}

// tagged puts host's tag before text.
func (m Model) tagged(host, text string) string {
	if tag := m.hostTag(host); tag != "" {
		return tag + " " + text
	}
	return text
}

// splitHost is the remembered host a host-qualified key names and the key that host knows; host is "" for this host's keys.
func (m Model) splitHost(key string) (host, rest string) {
	for _, h := range m.hostNames() {
		if after, ok := strings.CutPrefix(key, h+"/"); ok {
			return h, after
		}
	}
	return "", key
}

// projectHost is the host a mission runs on, "" for this one.
func (m Model) projectHost(p core.Project) string {
	for _, s := range p.Sessions {
		if s.Remote != "" {
			return s.Remote
		}
	}
	for _, d := range p.Dormant {
		if h, _ := m.splitHost(d.Key); h != "" {
			return h
		}
	}
	for _, it := range p.Items {
		if it.Host != "" {
			return it.Host
		}
	}
	return ""
}

// repoHost is the host every mission of a repository runs on, "" when any runs here.
func (m Model) repoHost(r core.Repo) string {
	host := ""
	for i, p := range r.Missions {
		h := m.projectHost(p)
		if h == "" || i > 0 && h != host {
			return ""
		}
		host = h
	}
	return host
}

func (m Model) needHost(n core.Need) string {
	switch {
	case n.Session != nil:
		return n.Session.Remote
	case n.Item != nil:
		return n.Item.Host
	}
	return ""
}

// where is where a session runs, its host by short name when that is not this one.
func (m Model) where(s core.Live) string {
	if s.Remote != "" {
		return m.tagged(s.Remote, s.Where)
	}
	return s.Location()
}

func (m Model) whereWidth(sessions []core.Live) int {
	w := 18
	for _, s := range sessions {
		w = max(w, min(30, ansi.StringWidth(m.where(s))))
	}
	return w
}

// hostState is how a host whose rows the board shows stands when that is worth saying: unreachable or not yet read; H says why.
func (m Model) hostState(host string) string {
	for _, r := range m.board.Remotes {
		if r.Host == host && (r.Err != nil || r.Stale || r.Late(m.board.At)) {
			return hostBrief(r, m.board.At)
		}
	}
	return ""
}

// hostBrief is how a host stands in a word or two: ok, checking, slow, behind or no answer, with how long.
func hostBrief(r core.RemoteBoard, now time.Time) string {
	switch {
	case r.Err != nil && !r.FailingSince.IsZero():
		return "no answer " + core.Span(now.Sub(r.FailingSince))
	case r.Err != nil && !r.At.IsZero():
		return "no answer " + core.Span(now.Sub(r.At))
	case r.Err != nil:
		return "unreachable"
	case r.At.IsZero(), r.Stale:
		return "checking"
	case r.Slow:
		return "slow " + core.Span(now.Sub(r.At))
	case r.Late(now):
		return "behind " + core.Span(now.Sub(r.At))
	}
	return "ok"
}

// hostsLine is each host's nickname and brief state, as many as fit the width; H shows the rest and why a host fails.
func (m Model) hostsLine() string {
	var hosts []string
	for _, r := range m.board.Remotes {
		line := m.hostTag(r.Host) + " " + hostBrief(r, m.board.At)
		switch {
		case r.Err != nil:
			line = pal.red.Render(line)
		case r.Slow || r.Late(m.board.At):
			line = pal.yellow.Render(line)
		}
		hosts = append(hosts, line)
	}
	render := func(n int) string {
		line := " " + pal.dim.Render("hosts ") + strings.Join(hosts[:n], "   ")
		if n < len(hosts) {
			line += pal.dim.Render(fmt.Sprintf("   +%d more (H)", len(hosts)-n))
		}
		return line
	}
	for n := len(hosts); n > 0; n-- {
		if line := render(n); m.width <= 0 || ansi.StringWidth(line) < m.width {
			return line
		}
	}
	return m.fit(render(0))
}

// fit cuts s to the board's width.
func (m Model) fit(s string) string {
	if m.width <= 0 {
		return s
	}
	return ansi.Truncate(s, max(20, m.width-1), "…")
}
