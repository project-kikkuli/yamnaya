package tui

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func (m Model) recoveryHosts() []string {
	hosts := []string{""}
	if m.core.Remotes != nil {
		hosts = append(hosts, m.core.Remotes.Hosts()...)
	}
	for _, r := range m.board.Remotes {
		hosts = append(hosts, r.Host)
	}
	for _, project := range m.board.Projects {
		for _, item := range project.Items {
			if item.Host != "" {
				hosts = append(hosts, item.Host)
			}
		}
	}
	slices.Sort(hosts)
	return slices.Compact(hosts)
}

func (d *settingsData) loadRecovery(st *core.Store, hosts []string) {
	d.actions = map[string][]core.Activation{}
	d.launches = map[string][]core.PendingLaunch{}
	d.recoveryErrors = map[string]string{}
	if st != nil {
		var actionsErr, launchesErr error
		d.actions[""], actionsErr = st.Activations()
		d.launches[""], launchesErr = st.PendingLaunches()
		if actionsErr != nil {
			d.recoveryErrors[""] = actionsErr.Error()
		}
		if launchesErr != nil {
			d.recoveryErrors[""] += " " + launchesErr.Error()
		}
		if known, err := st.KnownHosts(); err == nil {
			for _, host := range known {
				hosts = append(hosts, host.Name)
			}
		}
	}
	slices.Sort(hosts)
	for _, host := range slices.Compact(hosts) {
		if host != "" {
			d.recoverySources = append(d.recoverySources, host)
			d.recoveryErrors[host] = "reading recovery records…"
		}
	}
}

type recoveryMsg struct {
	epoch    uint64
	host     string
	actions  []core.Activation
	launches []core.PendingLaunch
	err      error
}

func (m *Model) loadNextRecovery() tea.Cmd {
	if m.pn.kind != panelSettings || len(m.pn.settings.recoverySources) == 0 {
		return nil
	}
	host := m.pn.settings.recoverySources[0]
	m.pn.settings.recoverySources = m.pn.settings.recoverySources[1:]
	run, epoch := m.pn.run, m.pn.epoch
	return func() tea.Msg {
		msg := recoveryMsg{epoch: epoch, host: host}
		actions, err := run(host, "", "action", "list", "--json")
		if err != nil {
			msg.err = err
			return msg
		}
		if err := json.Unmarshal([]byte(actions), &msg.actions); err != nil {
			msg.err = err
			return msg
		}
		launches, err := run(host, "", "launch", "list", "--json")
		if err != nil {
			msg.err = err
			return msg
		}
		msg.err = json.Unmarshal([]byte(launches), &msg.launches)
		return msg
	}
}

func recoveryRecord[T any](v T) string { raw, _ := json.MarshalIndent(v, "", "  "); return string(raw) }

func (m Model) recoveryRows() []prow {
	rows := []prow{{text: ""}, {text: pal.bold.Render("supervisor recovery and queued launches")}}
	hosts := []string{""}
	for host := range m.pn.settings.actions {
		hosts = append(hosts, host)
	}
	for host := range m.pn.settings.launches {
		hosts = append(hosts, host)
	}
	for host := range m.pn.settings.recoveryErrors {
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	count := 0
	for _, host := range slices.Compact(hosts) {
		source := "this host"
		if host != "" {
			source = m.hostTag(host)
		}
		if err := m.pn.settings.recoveryErrors[host]; err != "" {
			rows = append(rows, prow{text: pal.red.Render(source + ": " + err)})
		}
		for _, a := range m.pn.settings.actions[host] {
			if a.State == "done" || a.State == "superseded" || a.State == "dropped" {
				continue
			}
			key := "panel:action:" + host + ":" + a.ID
			text := fmt.Sprintf("%s action %s  %s  target %s", source, a.ID, a.State, a.Recipient)
			targetSession := a.TargetSession
			if targetSession == "" {
				targetSession = a.Session
			}
			detail := func() string {
				return fmt.Sprintf("Supervisor action %s\nSource: %s\nTarget: %s\nSession: %s\nState: %s\nAttempt: %d\nReason: %s\n\nOriginal requested action:\n%s\n\nDurable record:\n%s", a.ID, source, a.Recipient, targetSession, a.State, a.Attempt, a.Error, a.Payload, recoveryRecord(a))
			}
			retryable := a.State == "blocked" || a.State == "uncertain" || a.State == "claimed"
			rows = append(rows, m.recoveryRow(host, key, a.ID, "action", text, detail, func() string { return recoveryRecord(a) }, retryable))
			count++
		}
		for _, p := range m.pn.settings.launches[host] {
			key := "panel:pending-launch:" + host + ":" + p.ID
			text := fmt.Sprintf("%s launch %s  %s  %s", source, p.ID, p.State, oneLine(p.Reason+p.Result))
			target := p.Launch.Host
			if target == "" {
				target = source
			}
			detail := func() string {
				return fmt.Sprintf("Launch %s\nSource: %s\nTarget: %s\nState: %s\nReason: %s\nResult: %s\n\nOriginal prompt:\n%s\n\nPreserved launch:\n%s", p.ID, source, target, p.State, p.Reason, p.Result, p.Launch.Prompt, recoveryRecord(p.Launch))
			}
			rows = append(rows, m.recoveryRow(host, key, p.ID, "launch", text, detail, func() string { return recoveryRecord(p) }, p.State == "blocked"))
			count++
		}
	}
	if count == 0 {
		note := "no active supervisor actions or queued launches"
		if len(m.pn.settings.recoveryErrors) > 0 {
			note = "recovery records remain unread at the sources shown above"
		}
		rows = append(rows, prow{text: pal.dim.Render(note)})
	}
	return rows
}

func (m Model) recoveryRow(host, key, id, kind, text string, detail, revision func() string, retryable bool) prow {
	inspect := pdo{"enter", "inspect full original request and delivery state", func(m Model) (tea.Model, tea.Cmd) {
		if m.pn.inspected == nil {
			m.pn.inspected = map[string]string{}
		}
		m.pn.inspected[key] = revision()
		m.show("enter", detail()+"\n\nReturn to Y, then r explicitly retries recoverable requests after inspection.")
		return m, nil
	}}
	does := []pdo{inspect, {"r", "explicit retry after inspection", func(m Model) (tea.Model, tea.Cmd) {
		if !retryable {
			m.message = "This request is not awaiting explicit recovery."
			return m, nil
		}
		current := revision()
		if m.pn.inspected[key] != current {
			m.message = "Inspect the current request with enter before retrying."
			return m, nil
		}
		command := m.retryRecovery(host, key, id, kind, current)
		m.confirm("retry "+kind+" "+id+" after checking prior effects?", command)
		return m, nil
	}}}
	return prow{text: text, key: key, does: does}
}

func (m *Model) retryRecovery(host, key, id, kind, revision string) tea.Cmd {
	c, run := m.core, m.pn.run
	return m.act(key, "retrying "+kind, "retry", func() (string, error) {
		current, err := recoveryRevision(c.Store, run, host, kind, id)
		if err != nil {
			return "", err
		}
		if current != revision {
			return "", fmt.Errorf("recovery record changed; inspect its current state in Y before retrying")
		}
		if host != "" {
			return run(host, "", kind, "retry", id)
		}
		if kind == "action" {
			err := c.RetryAction(id)
			return "Supervisor action rechecked for retry.", err
		}
		if kind == "launch" {
			return c.RetryLaunch(id)
		}
		return "", fmt.Errorf("unsupported recovery kind %s", strings.TrimSpace(kind))
	})
}

func recoveryRevision(st *core.Store, run func(string, string, ...string) (string, error), host, kind, id string) (string, error) {
	switch kind {
	case "action":
		var rows []core.Activation
		var err error
		if host == "" {
			rows, err = st.Activations()
		} else {
			out, readErr := run(host, "", "action", "list", "--json")
			err = readErr
			if err == nil {
				err = json.Unmarshal([]byte(out), &rows)
			}
		}
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			if row.ID == id {
				return recoveryRecord(row), nil
			}
		}
	case "launch":
		var rows []core.PendingLaunch
		var err error
		if host == "" {
			rows, err = st.PendingLaunches()
		} else {
			out, readErr := run(host, "", "launch", "list", "--json")
			err = readErr
			if err == nil {
				err = json.Unmarshal([]byte(out), &rows)
			}
		}
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			if row.ID == id {
				return recoveryRecord(row), nil
			}
		}
	}
	return "", fmt.Errorf("recovery record %s no longer awaits recovery; reopen Y", id)
}
