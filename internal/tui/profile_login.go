package tui

import (
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type profileDraft struct {
	host       string
	p          core.LoginProfile
	registered bool
}

type profileAddedMsg struct {
	draft *profileDraft
	epoch uint64
	err   error
}

type profileLoginMsg struct {
	host, harness, id string
	epoch             uint64
	err               error
}

type profileSwitchedMsg struct {
	actMsg
	epoch  uint64
	target panelTarget
	id     string
	queued bool
}

type profileRebind struct{ from, to string }

func (m *Model) refreshProfileTarget() {
	t, change := m.pn.target, m.pn.rebind
	if t == nil || change == nil || t.session == "" {
		return
	}
	var matches []core.Live
	for _, s := range m.board.Sessions {
		if s.Remote != t.host || s.Harness != t.harness || s.ID != t.session {
			continue
		}
		if change.to == "auto" && profile.ID(s.Profile) == profile.ID(change.from) || change.to != "auto" && profile.ID(s.Profile) != profile.ID(change.to) {
			continue
		}
		matches = append(matches, s)
	}
	if len(matches) != 1 {
		return
	}
	s := matches[0]
	next := *t
	next.profile, next.root, next.cwd = s.Profile, s.ConfigRoot, s.Cwd
	next.ref = qual(t.host, t.harness+":"+profile.ID(s.Profile)+"@"+s.ID)
	m.pn.target, m.pn.rebind = &next, nil
}

func (m Model) profileSwitched(msg profileSwitchedMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil && msg.epoch == m.pn.epoch && m.pn.target != nil && m.pn.target.ref == msg.target.ref {
		t := *m.pn.target
		m.pn.rebind = &profileRebind{from: t.profile, to: msg.id}
		if !msg.queued && msg.id != "auto" && t.session != "" {
			t.profile = msg.id
			t.ref = qual(t.host, profile.SessionKey(t.harness, t.profile, t.session))
			t.root = ""
			ps := m.board.Profiles
			if t.host != "" {
				ps = nil
			}
			for _, r := range m.board.Remotes {
				if r.Host == t.host {
					ps = r.Profiles
				}
			}
			for _, p := range ps {
				if p.Harness == t.harness && p.ID == t.profile {
					t.root = p.Root
				}
			}
			m.pn.target = &t
		}
	}
	return m.ended(msg.actMsg)
}

func profileRowKey(host, harness, id string) string {
	return "panel:profile:" + qual(host, harness+"/"+profile.ID(id))
}

func (m *Model) focusProfile(host, harness, id string) {
	m.focusPanelRow(profileRowKey(host, harness, id))
}

func (m *Model) focusPanelRow(key string) {
	rows, _ := m.panelRows()
	for i, r := range rows {
		if r.key == key && len(r.does) > 0 {
			m.pn.cur = i
			return
		}
	}
	m.pn.cur = m.firstPanelRow(m.pn.cur, 1)
}

func (m *Model) contextProfile() {
	m.pn.harness, m.pn.host = "claude", ""
	m.pn.cur = m.firstPanelRow(0, 1)
	if t := m.pn.target; t != nil {
		m.pn.harness, m.pn.host = t.harness, t.host
		m.focusProfile(t.host, t.harness, t.profile)
	}
}

func (m Model) profileHost(host string) error {
	if host != "" && !slices.Contains(m.launchTargets(), host) {
		return fmt.Errorf("unknown remote %s; H adds or repairs it", host)
	}
	return m.silent(host)
}

func (m Model) profileRoot(d profileDraft) (tea.Model, tea.Cmd) {
	m.askLine("root of "+d.p.Harness+"/"+d.p.ID+m.onHost(d.host)+", absolute or ~/ there; enter registers then logs in; esc cancels", d.p.Root, false, func(m Model, root string) (tea.Model, tea.Cmd) {
		d.p.Root = root
		m.pn.added = &d
		key := profileRowKey(d.host, d.p.Harness, d.p.ID)
		run, epoch := m.pn.run, m.pn.epoch
		m.acts[key] = act{doing: "registering", what: "add", at: time.Now()}
		hostErr := m.profileHost(d.host)
		return m, func() tea.Msg {
			err := hostErr
			if err == nil {
				_, err = run(d.host, "", "profiles", "add", d.p.Harness, d.p.ID, "--label", d.p.Label, "--root", root)
			}
			return profileAddedMsg{draft: &d, epoch: epoch, err: err}
		}
	})
	return m, nil
}

func (m Model) profileAdded(msg profileAddedMsg) (tea.Model, tea.Cmd) {
	d := msg.draft
	key := profileRowKey(d.host, d.p.Harness, d.p.ID)
	m.acts[key] = act{what: "add", done: true, err: msg.err, at: time.Now()}
	if m.pn.epoch != msg.epoch || m.pn.added != d || m.pn.kind != panelProfiles || m.mode != modePanel {
		return m, m.refresh(false)
	}
	if msg.err != nil {
		m.message = "error: registration: " + msg.err.Error() + "; edit the root and retry, or esc then a for another label"
		return m.profileRoot(*d)
	}
	d.registered, d.p.State = true, "native login required; i retries authentication"
	m.focusProfile(d.host, d.p.Harness, d.p.ID)
	return m.loginProfile(d.host, d.p.Harness, d.p.ID)
}

func (m Model) loginProfile(host, harness, id string) (tea.Model, tea.Cmd) {
	if err := m.profileHost(host); err != nil {
		m.message = "error: " + err.Error() + "; i retries native login"
		return m, nil
	}
	args := []string{"profiles", "login", harness, id}
	if host != "" && harness == "codex" {
		args = append(args, "--device-auth")
	}
	epoch := m.pn.epoch
	return m, tea.Exec(attached{m.pn.tty(host, args...), m.out}, func(err error) tea.Msg {
		return profileLoginMsg{host: host, harness: harness, id: id, epoch: epoch, err: err}
	})
}

func (m Model) profileLoggedIn(msg profileLoginMsg) (tea.Model, tea.Cmd) {
	if m.pn.epoch != msg.epoch || m.pn.kind != panelProfiles || m.mode != modePanel {
		return m, m.refresh(false)
	}
	m.focusProfile(msg.host, msg.harness, msg.id)
	name := msg.harness + "/" + msg.id + m.onHost(msg.host)
	if msg.err != nil {
		m.message = "error: native login for " + name + ": " + msg.err.Error() + "; i retries here"
	} else {
		m.message = "native login finished for " + name + "; i logs in again"
		if t := m.pn.target; t != nil && t.host == msg.host && t.harness == msg.harness {
			m.message += "; enter switches only " + t.label + " to this login"
		} else {
			m.message += "; select a session and press B to switch it"
		}
	}
	if d := m.pn.added; d != nil && d.host == msg.host && d.p.Harness == msg.harness && d.p.ID == msg.id {
		d.p.State = m.message
	}
	return m, m.refresh(false)
}
