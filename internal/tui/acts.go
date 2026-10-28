package tui

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// act is an action the board dispatched on one row, here or on its host, and how it ended.
type act struct {
	// doing names it while it runs, such as "killing"; what names it once it ends, such as "kill".
	doing, what string
	at          time.Time
	done        bool
	message     string
	err         error
}

type actMsg struct {
	key     string
	message string
	err     error
}

// How long a row keeps saying how its action ended.
const (
	actShown   = 20 * time.Second
	actFailure = 2 * time.Minute
)

// act runs f off the board for the row keyed key, which shows doing until f ends and then how it ended.
func (m *Model) act(key, doing, what string, f func() (string, error)) tea.Cmd {
	m.acts[key] = act{doing: doing, what: what, at: time.Now()}
	return func() tea.Msg {
		msg, err := f()
		return actMsg{key, msg, err}
	}
}

// reachedMsg is a host that answered, so the session on it can take over this terminal.
type reachedMsg struct {
	key  string
	open *exec.Cmd
	t    core.Target
}

// attachRemote checks off the board that t's host answers before handing it this terminal, so a host that went silent fails
// on the row instead of holding the screen.
func (m Model) attachRemote(t core.Target, open *exec.Cmd) tea.Cmd {
	key, c := core.RemoteKey(t.Remote, t.Session.Key()), m.core
	if err := m.silent(t.Remote); err != nil {
		m.acts[key] = act{what: "open", at: time.Now(), done: true, err: err}
		return func() tea.Msg { return actMsg{key, "", err} }
	}
	m.acts[key] = act{doing: "reaching " + m.hostTag(t.Remote), what: "open", at: time.Now()}
	return func() tea.Msg {
		if err := c.Reach(t.Remote); err != nil {
			return actMsg{key, "", err}
		}
		return reachedMsg{key, open, t}
	}
}

// refuse ends the row's action at once with err, reaching no host.
func (m *Model) refuse(key, what string, err error) {
	m.acts[key] = act{what: what, at: time.Now(), done: true, err: err}
	m.message = "error: " + what + ": " + err.Error()
}

// silent is why host, when it is another one, did not answer ym's latest read of it.
func (m Model) silent(host string) error {
	if host == "" || m.core == nil {
		return nil
	}
	if err := m.core.Silent(host); err != nil {
		return fmt.Errorf("%s not answering: %s", m.hostTag(host), m.hostState(host))
	}
	return nil
}

// sessionKey is the key the board's actions on s go by, qualified by its host when that is another one.
func sessionKey(s core.Live) string {
	if s.Remote != "" {
		return core.RemoteKey(s.Remote, s.Key())
	}
	return s.Key()
}

// queued are the board's own commands waiting for a host that did not answer, by the row key they act on: the session as
// the command named it and, when b knows that session by a launch key or shorter id, by its full id.
func queued(relays []core.Relay, b core.Board) map[string]core.Relay {
	out := map[string]core.Relay{}
	for _, r := range relays {
		var y struct {
			Args []string `json:"args"`
		}
		if r.Kind != core.RelayYM || r.Origin != "" || json.Unmarshal([]byte(r.Body), &y) != nil || len(y.Args) < 2 {
			continue
		}
		var ref string
		switch y.Args[0] {
		case "kill":
			ref = y.Args[len(y.Args)-1]
		case "send":
			ref = y.Args[1]
		default:
			continue
		}
		out[core.RemoteKey(r.Dest, ref)] = r
		if id := sessionOn(b, r.Dest, ref); id != "" {
			out[core.RemoteKey(r.Dest, id)] = r
		}
	}
	return out
}

// sessionOn is the full id of the session on host that ref names by launch key, id or shorthand, or "".
func sessionOn(b core.Board, host, ref string) string {
	for _, l := range b.Launched {
		if l.Key == core.RemoteKey(host, ref) && l.Session != "" {
			return l.Session
		}
	}
	var ids []string
	for _, s := range b.Sessions {
		if s.Remote == host {
			ids = append(ids, s.ID)
		}
	}
	id, _ := core.SessionRef(ref, ids)
	return id
}

// note is how the latest action on the rows keyed keys stands, "" when none is worth saying.
func (m Model) note(keys ...string) string {
	now := time.Now()
	for _, key := range keys {
		if r, ok := m.queued[key]; ok {
			var y struct {
				Args []string `json:"args"`
			}
			json.Unmarshal([]byte(r.Body), &y)
			if r.Error != "" {
				return pal.red.Render(y.Args[0] + " refused on " + m.hostTag(r.Dest) + ", sent again until it lands: " + oneLine(r.Error))
			}
			why := "waiting for ym serve to carry it"
			if err := m.silent(r.Dest); err != nil {
				why = err.Error()
			}
			return pal.yellow.Render(y.Args[0] + " pending since " + r.Created.Local().Format(time.Kitchen) + ": " + why)
		}
	}
	for _, key := range keys {
		a, ok := m.acts[key]
		switch {
		case !ok:
		case !a.done:
			return pal.yellow.Render(fmt.Sprintf("%s… %ds", a.doing, int(now.Sub(a.at).Seconds())))
		case a.err != nil && now.Sub(a.at) < actFailure:
			return pal.red.Render(a.what + " failed: " + oneLine(a.err.Error()))
		case a.err == nil && now.Sub(a.at) < actShown:
			first, _, _ := strings.Cut(a.message, "\n")
			return pal.dim.Render(first)
		}
	}
	return ""
}

// noting reports whether any row has an action worth saying, so the board redraws as it changes.
func (m Model) noting() bool {
	if len(m.queued) > 0 {
		return true
	}
	now := time.Now()
	for _, a := range m.acts {
		if !a.done || now.Sub(a.at) < actFailure {
			return true
		}
	}
	return false
}

// withNote puts note on its own line under text's first line.
func withNote(text, note string) string {
	if note == "" {
		return text
	}
	first, rest, _ := strings.Cut(text, "\n")
	return first + "\n         " + note + "\n" + rest
}

// rowNote is how the latest action on the sessions or goal behind r stands.
func (m Model) rowNote(r row) string {
	var keys []string
	switch {
	case r.node != nil:
		keys = append(keys, "node:"+r.node.Manager.ID)
	case r.need != nil && r.need.Session != nil:
		keys = append(keys, sessionKey(*r.need.Session))
	case r.session != nil:
		keys = append(keys, sessionKey(*r.session))
	case r.project != nil && r.item == nil:
		for _, s := range r.project.Sessions {
			keys = append(keys, sessionKey(s))
		}
		for _, d := range r.project.Dormant {
			keys = append(keys, d.Key)
		}
	}
	return m.note(keys...)
}

func (m Model) ended(msg actMsg) (tea.Model, tea.Cmd) {
	a := m.acts[msg.key]
	a.done, a.message, a.err, a.at = true, msg.message, msg.err, time.Now()
	m.acts[msg.key] = a
	if msg.err != nil {
		m.message = "error: " + msg.err.Error()
	} else {
		m.message = msg.message
	}
	if m.listMode() == modePanel && m.pn.kind == panelSettings {
		return m, tea.Batch(m.refresh(false), m.loadSettings())
	}
	return m, m.refresh(false)
}
