package tui

import (
	"net"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// shortHosts names each host by the shortest dash-separated tail of its first DNS label that no other host's label ends in.
func shortHosts(hosts []string) map[string]string {
	label := func(h string) string {
		if _, after, ok := strings.Cut(h, "@"); ok {
			h = after
		}
		if net.ParseIP(h) != nil {
			return h
		}
		first, _, _ := strings.Cut(h, ".")
		return first
	}
	out := map[string]string{}
	for _, h := range hosts {
		own := label(h)
		parts := strings.Split(own, "-")
		out[h] = h
		for k := 1; k <= len(parts); k++ {
			tail := strings.Join(parts[len(parts)-k:], "-")
			if !slices.ContainsFunc(hosts, func(o string) bool {
				return o != h && (label(o) == tail || strings.HasSuffix(label(o), "-"+tail))
			}) {
				out[h] = tail
				break
			}
		}
	}
	return out
}

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

// hostTag marks a row from another host with that host's short name; this host's rows carry none.
func (m Model) hostTag(host string) string {
	if host == "" {
		return ""
	}
	return "@" + shortHosts(m.hostNames())[host]
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

// hostState is how a host whose rows the board shows stands when that is worth saying: unreachable or not yet read.
func (m Model) hostState(host string) string {
	for _, r := range m.board.Remotes {
		if r.Host == host && (r.Err != nil || r.Stale || r.Late(m.board.At)) {
			return r.StateAt(m.board.At)
		}
	}
	return ""
}
