package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// panelKind is a screen behind ? listing things to act on: login profiles, hosts, ym's own settings, or every launch.
type panelKind int

const (
	panelNone panelKind = iota
	panelProfiles
	panelHosts
	panelSettings
	panelLaunched
)

// panelKeys are the board keys that open each panel; the same key closes it.
var panelKeys = map[string]panelKind{"B": panelProfiles, "H": panelHosts, "Y": panelSettings, "E": panelLaunched}

type panelState struct {
	kind panelKind
	cur  int
	// target is the session B was pressed on; enter on a profile switches it to that login.
	target *panelTarget
	in     *panelInput
	// harness and host are the new profile's, which tab and ctrl+t change while its label is typed.
	harness, host string
	settings      settingsData
	relays        []core.Relay
	// run runs the ym command a user would type, on host ("" for this one), with stdin; tty hands it this terminal.
	run func(host, stdin string, args ...string) (string, error)
	tty func(host string, args ...string) *exec.Cmd
}

type panelTarget struct{ ref, label, harness, host string }

// panelInput is a line a panel asks for; secret hides what is typed.
type panelInput struct {
	label  string
	secret bool
	pick   bool
	then   func(m Model, text string) (tea.Model, tea.Cmd)
}

type settingsData struct {
	loaded    bool
	service   map[string]any
	docs      string
	cap       int64
	authority map[string]bool
	err       error
}

type settingsMsg settingsData

// prow is one line of a panel; rows with no actions are headings or notes the cursor skips.
type prow struct {
	text string
	// key names the row's actions in m.acts, so it shows how the latest one stands.
	key  string
	does []pdo
}

type pdo struct {
	key, what string
	run       func(m Model) (tea.Model, tea.Cmd)
}

func selfRunner(c *core.Core) func(host, stdin string, args ...string) (string, error) {
	return func(host, stdin string, args ...string) (string, error) {
		if host != "" {
			return c.RunRemote(host, args...)
		}
		self, err := os.Executable()
		if err != nil {
			return "", err
		}
		cmd := exec.Command(self, args...)
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		text := strings.TrimSpace(string(out))
		if err != nil && text != "" {
			return "", errors.New(strings.TrimPrefix(text, "ym: "))
		}
		return text, err
	}
}

func selfTTY(c *core.Core) func(host string, args ...string) *exec.Cmd {
	return func(host string, args ...string) *exec.Cmd {
		if host != "" && c.Remotes != nil {
			return c.Remotes.Attach(host, args...)
		}
		self, _ := os.Executable()
		cmd := exec.Command(self, args...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd
	}
}

// openPanel shows kind, aimed at the session on row r when it has one.
func (m Model) openPanel(kind panelKind, r row) (tea.Model, tea.Cmd) {
	m.pn.kind, m.pn.cur, m.pn.target = kind, 0, nil
	s := m.sessionOf(r)
	if s == nil && r.project != nil && r.session == nil && r.item == nil && len(r.project.Sessions) == 1 {
		s = &r.project.Sessions[0]
	}
	if s != nil {
		ref := s.Ref()
		if s.Remote != "" {
			ref = core.RemoteKey(s.Remote, ref)
		}
		m.pn.target = &panelTarget{ref: ref, label: s.Label(), harness: s.Harness, host: s.Remote}
	} else if r.dormant != nil {
		host, _ := m.splitHost(r.dormant.Key)
		m.pn.target = &panelTarget{ref: r.dormant.Key, label: r.dormant.Title, harness: r.dormant.Harness, host: host}
	}
	m.push(modePanel)
	m.pn.cur = m.firstPanelRow(0, 1)
	if kind == panelSettings {
		return m, m.loadSettings()
	}
	return m, nil
}

func (m Model) panelKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	rows, global := m.panelRows()
	switch key := k.String(); key {
	case "esc", "q":
		m.pop()
		return m, nil
	case "up", "k":
		m.pn.cur = m.firstPanelRow(m.pn.cur-1, -1)
		return m, nil
	case "down", "j":
		m.pn.cur = m.firstPanelRow(m.pn.cur+1, 1)
		return m, nil
	case "?":
		m.back, m.mode, m.scrollY = m.mode, modeHelp, 0
		return m, nil
	default:
		if panelKeys[key] == m.pn.kind {
			m.pop()
			return m, nil
		}
		var does []pdo
		if m.pn.cur >= 0 && m.pn.cur < len(rows) {
			does = rows[m.pn.cur].does
		}
		for _, d := range append(does, global...) {
			if d.key == key {
				m.message = ""
				return d.run(m)
			}
		}
	}
	return m, nil
}

// firstPanelRow is the nearest row from i in direction step that takes a key, else the cursor as it is.
func (m Model) firstPanelRow(i, step int) int {
	rows, _ := m.panelRows()
	for ; i >= 0 && i < len(rows); i += step {
		if len(rows[i].does) > 0 {
			return i
		}
	}
	if m.pn.cur >= 0 && m.pn.cur < len(rows) && len(rows[m.pn.cur].does) > 0 {
		return m.pn.cur
	}
	for i := range rows {
		if len(rows[i].does) > 0 {
			return i
		}
	}
	return 0
}

func (m Model) panelRows() ([]prow, []pdo) {
	switch m.pn.kind {
	case panelProfiles:
		return m.profileRows()
	case panelHosts:
		return m.hostRows()
	case panelSettings:
		return m.settingRows()
	case panelLaunched:
		return m.launchRows()
	}
	return nil, nil
}

var panelTitles = map[panelKind]string{panelProfiles: "LOGIN PROFILES", panelHosts: "HOSTS", panelSettings: "SETTINGS", panelLaunched: "LAUNCHED"}

func (m Model) panelView() viewPage {
	b := viewPage{}
	rows, _ := m.panelRows()
	sub := map[panelKind]string{panelProfiles: "every Claude and Codex login on every host, with its limit windows",
		panelHosts: "every remembered host and what this host still carries to them", panelSettings: "ym serve, Notion, the context cap, merge authority",
		panelLaunched: "what ym launched, and every ended session or killed goal ym revive brings back"}[m.pn.kind]
	b.WriteString(pal.bold.Render(" "+panelTitles[m.pn.kind]) + pal.dim.Render("  "+sub) + "\n")
	if t := m.pn.target; t != nil && m.pn.kind == panelProfiles {
		b.WriteString(pal.dim.Render("   enter switches "+m.tagged(t.host, t.label)+" to the login under the cursor") + "\n")
	}
	b.WriteString("\n")
	for i, r := range rows {
		line := "   " + r.text
		if len(r.does) == 0 {
			line = " " + r.text
		} else if i == m.pn.cur {
			line = " > " + r.text
		}
		if r.key != "" {
			line = withNote(line, m.note(r.key))
		}
		b.WriteString(line + "\n")
	}
	return b
}

// panelHints are the keys the row under the cursor and the panel take.
func (m Model) panelHints() string {
	rows, global := m.panelRows()
	var does []pdo
	if m.pn.cur >= 0 && m.pn.cur < len(rows) {
		does = rows[m.pn.cur].does
	}
	var hints []string
	for _, d := range append(does, global...) {
		hints = append(hints, d.key+" "+d.what)
	}
	return " " + strings.Join(append(hints, "esc back"), hintSep)
}

// runYM runs ym args on host off the board, the row keyed key saying doing until it ends.
func (m *Model) runYM(key, doing, what, host, stdin string, args ...string) tea.Cmd {
	run := m.pn.run
	return m.act(key, doing, what, func() (string, error) { return run(host, stdin, args...) })
}

// ask opens the panel's input line; then gets what was typed.
func (m *Model) askLine(label, initial string, secret bool, then func(Model, string) (tea.Model, tea.Cmd)) {
	m.pn.in = &panelInput{label: label, secret: secret, then: then}
	m.startInput(inPanel, "", "", initial, false)
}

// handOver gives this terminal to ym args on host, coming back to the board when it ends.
func (m Model) handOver(host, back string, args ...string) (tea.Model, tea.Cmd) {
	if err := m.silent(host); err != nil {
		m.message = "error: " + err.Error()
		return m, nil
	}
	cmd := m.pn.tty(host, args...)
	return m, tea.Exec(attached{cmd, m.out}, func(err error) tea.Msg { return doneMsg{back, err} })
}

func (m Model) profileRows() ([]prow, []pdo) {
	var rows []prow
	add := pdo{"a", "add a login", func(m Model) (tea.Model, tea.Cmd) { return m.addProfile() }}
	if t := m.pn.target; t != nil {
		ref := t.ref
		rows = append(rows, prow{text: "auto: switch " + t.label + " to a different verified login", key: "panel:switch:" + ref,
			does: []pdo{{"enter", "switch", func(m Model) (tea.Model, tea.Cmd) { return m.switchProfile(ref, "auto") }}}})
	}
	group := func(host string, ps []core.LoginProfile, limits []core.Limit) {
		name := "local " + m.host
		if host != "" {
			name = m.tagged(host, host)
		}
		rows = append(rows, prow{text: pal.bold.Render(name)})
		if len(ps) == 0 {
			rows = append(rows, prow{text: pal.dim.Render("   no profiles read from it yet")})
		}
		for _, p := range ps {
			rows = append(rows, m.profileRow(host, p, limits))
		}
	}
	group("", m.board.Profiles, m.board.Fleet.Limits)
	for _, r := range m.board.Remotes {
		var limits []core.Limit
		if r.Usage != nil {
			limits = r.Usage.Limits
		}
		group(r.Host, r.Profiles, limits)
	}
	return rows, []pdo{add}
}

func (m Model) profileRow(host string, p core.LoginProfile, limits []core.Limit) prow {
	id := profileDisplay(p.Harness, p.ID)
	if p.ID == "default" {
		id = p.Harness + "/default"
	}
	account := p.Account
	if account == "" {
		account = "identity unknown"
	}
	var windows []string
	for _, l := range limits {
		if l.Harness != p.Harness || (l.Account != "" && l.Account != p.Account) || (l.Account == "" && profile.ID(l.Profile) != p.ID) {
			continue
		}
		w := fmt.Sprintf("%s %.0f%%", l.Label(), l.Used)
		if !l.ResetsAt.IsZero() {
			w += " resets " + l.ResetsAt.Local().Format("Jan 2 3:04PM")
		}
		windows = append(windows, w)
	}
	state := p.State
	if len(windows) > 0 {
		state = strings.Join(windows, " · ")
	}
	text := pad(id, 18) + " " + pad(p.Label, 12) + " " + pad(account, 26) + " " + state
	if p.Default {
		text += pal.green.Render("  launch default")
	}
	if p.Root != "" {
		text += pal.dim.Render("  " + p.Root)
	}
	key := "panel:profile:" + qual(host, p.Harness+"/"+p.ID)
	h, pid := p.Harness, p.ID
	var does []pdo
	if t := m.pn.target; t != nil && t.harness == h && t.host == host {
		ref := t.ref
		does = append(does, pdo{"enter", "switch " + t.label + " to it", func(m Model) (tea.Model, tea.Cmd) { return m.switchProfile(ref, pid) }})
	}
	does = append(does, pdo{"i", "log in", func(m Model) (tea.Model, tea.Cmd) {
		args := []string{"profiles", "login", h, pid}
		if host != "" && h == "codex" {
			args = append(args, "--device-auth")
		}
		return m.handOver(host, "back from logging in to "+h+"/"+pid, args...)
	}})
	if !p.Default {
		does = append(does, pdo{"d", "make it the launch default", func(m Model) (tea.Model, tea.Cmd) {
			return m, m.runYM(key, "setting the default", "default", host, "", "profiles", "default", h, pid)
		}})
	}
	if pid != "default" {
		does = append(does, pdo{"x", "remove", func(m Model) (tea.Model, tea.Cmd) {
			m.confirm("remove "+h+"/"+pid+m.onHost(host)+"? its root and login stay on disk", m.runYM(key, "removing", "remove", host, "", "profiles", "remove", h, pid))
			return m, nil
		}})
	}
	return prow{text: text, key: key, does: does}
}

// qual is key qualified by host, unchanged on this host.
func qual(host, key string) string {
	if host == "" {
		return key
	}
	return core.RemoteKey(host, key)
}

func (m Model) onHost(host string) string {
	if host == "" {
		return ""
	}
	return " on " + m.hostTag(host)
}

// switchProfile resumes the session ref names under the login id, as ym switch --profile does.
func (m Model) switchProfile(ref, id string) (tea.Model, tea.Cmd) {
	switcher, ok := m.core.Launches.(interface {
		SwitchProfile(string, string) (string, error)
	})
	if !ok || switcher == nil {
		m.message = "error: ym's launcher is unavailable"
		return m, nil
	}
	return m, m.act("panel:switch:"+ref, "switching", "switch", func() (string, error) { return switcher.SwitchProfile(ref, id) })
}

var notID = regexp.MustCompile(`[^a-z0-9_-]+`)

// profileID is the profile id a label names: its letters, digits, dashes and underscores.
func profileID(label string) string {
	return strings.Trim(notID.ReplaceAllString(strings.ToLower(label), "-"), "-_")
}

// addProfile asks for the new login's label, tab switching its harness and ctrl+t its host, then its root.
func (m Model) addProfile() (tea.Model, tea.Cmd) {
	if m.pn.harness == "" {
		m.pn.harness = "claude"
	}
	if !slices.Contains(m.launchTargets(), m.pn.host) {
		m.pn.host = ""
	}
	m.askLine("", "", false, func(m Model, label string) (tea.Model, tea.Cmd) {
		id := profileID(label)
		if id == "" || id == "default" {
			m.message = "error: a label needs a letter or digit, and default is the native login"
			return m, nil
		}
		h, host := m.pn.harness, m.pn.host
		m.askLine("root of "+h+"/"+id+m.onHost(host)+", an absolute directory there", m.defaultRoot(host, h, id), false, func(m Model, root string) (tea.Model, tea.Cmd) {
			if root == "" {
				return m, nil
			}
			key := "panel:profile:" + qual(host, h+"/"+id)
			m.message = "adding " + h + "/" + id + m.onHost(host) + "; i on it logs in"
			return m, m.runYM(key, "adding", "add", host, "", "profiles", "add", h, id, "--label", label, "--root", root)
		})
		return m, nil
	})
	m.pn.in.pick = true
	return m, nil
}

// defaultRoot is ~/.<harness>-<id> on host, absolute when ym knows that host's home, else under ~/ for that host's ym to expand.
func (m Model) defaultRoot(host, harness, id string) string {
	home := ""
	if host == "" {
		home, _ = os.UserHomeDir()
	}
	for _, r := range m.board.Remotes {
		if r.Host != host {
			continue
		}
		for _, p := range r.Profiles {
			if p.Harness == harness && p.ID == "default" && filepath.Base(p.Root) == "."+harness {
				home = filepath.Dir(p.Root)
			}
		}
	}
	if home == "" {
		return "~/." + harness + "-" + id
	}
	return filepath.Join(home, "."+harness+"-"+id)
}

func (m Model) panelLabel() string {
	in := m.pn.in
	if in == nil {
		return ""
	}
	if !in.pick {
		return in.label
	}
	on := "local"
	if m.pn.host != "" {
		on = m.hostTag(m.pn.host)
	}
	label := "new " + m.pn.harness + " login on " + on + ", its label" + pal.dim.Render(" [tab: "+core.Other(m.pn.harness)+"]")
	if len(m.launchTargets()) > 1 {
		label += pal.dim.Render(" [ctrl+t: another host]")
	}
	return label
}

// panelInputKey takes tab and ctrl+t while a new profile's label is typed.
func (m Model) panelInputKey(k tea.KeyPressMsg) (Model, bool) {
	if m.pn.in == nil || !m.pn.in.pick {
		return m, false
	}
	switch k.String() {
	case "tab":
		m.pn.harness = core.Other(m.pn.harness)
		return m, true
	case "ctrl+t":
		ts := m.launchTargets()
		m.pn.host = ts[(slices.Index(ts, m.pn.host)+1)%len(ts)]
		return m, true
	}
	return m, false
}

func (m Model) hostRows() ([]prow, []pdo) {
	var rows []prow
	add := pdo{"a", "add a host", func(m Model) (tea.Model, tea.Cmd) {
		m.askLine("host to install ym on and remember (an ssh name)", "", false, func(m Model, host string) (tea.Model, tea.Cmd) {
			if host == "" {
				return m, nil
			}
			return m, m.runYM("panel:host:"+host, "installing ym on "+host, "install", "", "", "remote", "install", host)
		})
		return m, nil
	}}
	var hosts []string
	if m.core != nil && m.core.Remotes != nil {
		hosts = m.core.Remotes.Hosts()
	}
	if len(hosts) == 0 {
		rows = append(rows, prow{text: pal.dim.Render("no hosts yet; a adds one")})
	}
	for _, host := range hosts {
		state, sessions := "not read yet", 0
		for _, r := range m.board.Remotes {
			if r.Host == host {
				state, sessions = r.StateAt(m.board.At), len(r.Sessions)
			}
		}
		key := "panel:host:" + host
		rows = append(rows, prow{text: pad(m.hostTag(host), 8) + " " + host + "  " + pad(plural(sessions, "session"), 12) + " " + state, key: key, does: []pdo{
			{"i", "install or update ym there, kept running", func(m Model) (tea.Model, tea.Cmd) {
				return m, m.runYM(key, "installing ym on "+host, "install", "", "", "remote", "install", host)
			}},
			{"o", "open its board", func(m Model) (tea.Model, tea.Cmd) { return m.handOver(host, "back from "+host) }},
			{"x", "forget", func(m Model) (tea.Model, tea.Cmd) {
				m.confirm("forget "+host+"? its sessions leave this board until ym reaches it again", m.runYM(key, "forgetting", "forget", "", "", "remote", "forget", host))
				return m, nil
			}},
		}})
	}
	if len(m.pn.relays) > 0 {
		rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("still to carry")})
	}
	for _, r := range m.pn.relays {
		seq := fmt.Sprint(r.Seq)
		text := pad(m.hostTag(r.Dest), 8) + " " + relayWhat(r) + pal.dim.Render(" since "+r.Created.Local().Format(time.Kitchen))
		if r.Error != "" {
			text += pal.red.Render("  refused there: " + oneLine(r.Error))
		}
		key := "panel:relay:" + seq
		rows = append(rows, prow{text: text, key: key, does: []pdo{{"x", "stop carrying it", func(m Model) (tea.Model, tea.Cmd) {
			m.confirm("stop carrying "+relayWhat(r)+" to "+r.Dest+"?", m.runYM(key, "dropping", "drop", "", "", "relay", "drop", seq))
			return m, nil
		}}}})
	}
	return rows, []pdo{add}
}

// loadSettings reads ym serve's status, Notion's, the context cap and merge authority off the board.
func (m Model) loadSettings() tea.Cmd {
	run, st := m.pn.run, m.core.Store
	return func() tea.Msg {
		d := settingsData{loaded: true}
		out, err := run("", "", "service", "status")
		if err == nil && json.Unmarshal([]byte(out), &d.service) != nil {
			d.service = map[string]any{"error": out}
		}
		docs, docsErr := run("", "", "docs")
		d.docs, _, _ = strings.Cut(docs, "\n")
		var capErr, authErr error
		if st != nil {
			d.cap, capErr = st.ContextCap()
			d.authority, authErr = st.Authority()
		}
		d.err = errors.Join(err, docsErr, capErr, authErr)
		return settingsMsg(d)
	}
}

func (m Model) settingRows() ([]prow, []pdo) {
	d := m.pn.settings
	if !d.loaded {
		return []prow{{text: pal.dim.Render("reading…")}}, nil
	}
	var rows []prow
	if d.err != nil {
		rows = append(rows, prow{text: pal.red.Render("could not read everything: " + oneLine(d.err.Error()))})
	}
	rows = append(rows, prow{text: pal.bold.Render("ym serve on this host")})
	serve := "not running"
	if running, _ := d.service["running"].(bool); running {
		serve = "running"
	}
	if at, ok := d.service["at"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			serve += ", last cycle " + age(time.Now(), t)
		}
	}
	if e, _ := d.service["error"].(string); e != "" {
		serve += pal.red.Render(", last error: " + oneLine(e))
	}
	rows = append(rows, prow{text: serve, key: "panel:set:restart", does: []pdo{{"enter", "restart it with the board's environment", func(m Model) (tea.Model, tea.Cmd) {
		m.confirm("restart ym serve? agents keep running", m.runYM("panel:set:restart", "restarting ym serve", "restart", "", "", "service", "restart"))
		return m, nil
	}}}})
	kept, _ := d.service["kept"].(string)
	keep := "kept running across logouts and reboots"
	if kept == "" {
		keep = "not kept by launchd: it runs while a board or ym command keeps it up"
	}
	keepDo := pdo{"enter", "keep it running", func(m Model) (tea.Model, tea.Cmd) {
		return m, m.runYM("panel:set:keep", "keeping ym serve", "keep", "", "", "service", "keep")
	}}
	if kept != "" {
		keepDo = pdo{"enter", "stop keeping it", func(m Model) (tea.Model, tea.Cmd) {
			return m, m.runYM("panel:set:keep", "unkeeping ym serve", "unkeep", "", "", "service", "unkeep")
		}}
	}
	rows = append(rows, prow{text: keep, key: "panel:set:keep", does: []pdo{keepDo}})
	rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("Notion")})
	rows = append(rows, prow{text: d.docs, key: "panel:set:docs", does: []pdo{{"enter", "connect an integration token", func(m Model) (tea.Model, tea.Cmd) {
		m.askLine("your Notion email", "", false, func(m Model, email string) (tea.Model, tea.Cmd) {
			if email == "" {
				return m, nil
			}
			m.askLine("Notion integration token for "+email+" (hidden)", "", true, func(m Model, token string) (tea.Model, tea.Cmd) {
				if token == "" {
					return m, nil
				}
				return m, m.runYM("panel:set:docs", "connecting Notion", "connect", "", token+"\n", "docs", "connect", email)
			})
			return m, nil
		})
		return m, nil
	}}}})
	rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("context cap")})
	limit := "off: ym never restarts managers and chiefs for their context"
	if d.cap > 0 {
		limit = core.Kilo(d.cap) + " tokens: an idle manager or chief past it restarts fresh from its brief when a wake is due"
	}
	rows = append(rows, prow{text: limit, key: "panel:set:cap", does: []pdo{{"enter", "change it", func(m Model) (tea.Model, tea.Cmd) {
		initial := "off"
		if d.cap > 0 {
			initial = core.Kilo(d.cap)
		}
		m.askLine("context cap (100000, 150k, 1.5M or off)", initial, false, func(m Model, text string) (tea.Model, tea.Cmd) {
			if text == "" {
				return m, nil
			}
			return m, m.runYM("panel:set:cap", "setting the cap", "context cap", "", "", "goal", "context-cap", text)
		})
		return m, nil
	}}}})
	rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("merge authority") + pal.dim.Render("  managers merge their own green pull requests only where cleared")})
	repos := map[string]bool{}
	for r := range d.authority {
		repos[r] = true
	}
	for _, r := range m.board.Repos {
		if r.Name != "" {
			repos[strings.ToLower(r.Name)] = true
		}
	}
	names := make([]string, 0, len(repos))
	for r := range repos {
		names = append(names, r)
	}
	sort.Strings(names)
	for _, repo := range names {
		can := d.authority[repo]
		text := pad(repo, 24) + " managers may not merge"
		flag, verb := "--merge", "let managers merge"
		if can {
			text = pad(repo, 24) + pal.green.Render(" managers may merge")
			flag, verb = "--no-merge", "send its merges to you"
		}
		key := "panel:set:authority:" + repo
		rows = append(rows, prow{text: text, key: key, does: []pdo{{"enter", verb, func(m Model) (tea.Model, tea.Cmd) {
			return m, m.runYM(key, "setting authority", "authority", "", "", "goal", "authority", repo, flag)
		}}}})
	}
	rows = append(rows, prow{text: pal.dim.Render("add a repository by name…"), key: "panel:set:authority", does: []pdo{{"enter", "clear a repository for merges", func(m Model) (tea.Model, tea.Cmd) {
		m.askLine("repository whose managers may merge (its name, such as yamnaya)", "", false, func(m Model, repo string) (tea.Model, tea.Cmd) {
			if repo == "" {
				return m, nil
			}
			return m, m.runYM("panel:set:authority", "setting authority", "authority", "", "", "goal", "authority", repo, "--merge")
		})
		return m, nil
	}}}})
	rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("ym itself") + pal.dim.Render("  "+revision())})
	rows = append(rows, prow{text: "update ym to the latest version", key: "panel:set:update", does: []pdo{{"enter", "update", func(m Model) (tea.Model, tea.Cmd) {
		m.confirm("update ym? the board and ym serve restart into it", m.runYM("panel:set:update", "updating ym", "update", "", "", "update"))
		return m, nil
	}}}})
	return rows, nil
}

func (m Model) launchRows() ([]prow, []pdo) {
	var running, ended []prow
	// revive runs ym revive key on runOn, checking first that host, where the session or goal lives, answers.
	revive := func(host, runOn, key string) pdo {
		return pdo{"enter", "revive", func(m Model) (tea.Model, tea.Cmd) {
			if err := m.silent(host); err != nil {
				m.message = "error: " + err.Error()
				return m, nil
			}
			return m, m.runYM("panel:launch:"+qual(runOn, key), "reviving "+key, "revive", runOn, "", "revive", key)
		}}
	}
	type launch struct {
		host string
		l    core.Launched
	}
	var all []launch
	for _, l := range m.board.Launched {
		all = append(all, launch{"", l})
	}
	for _, r := range m.board.Remotes {
		for _, l := range r.Launched {
			all = append(all, launch{r.Host, l})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].l.Landed.After(all[j].l.Landed) })
	for _, a := range all {
		l, host := a.l, a.host
		_, shown := m.splitHost(l.Key)
		text := pad(m.hostTag(host), 6) + " " + pad(l.Harness, 6) + " " + pad(shown, 30) + " "
		key := "panel:launch:" + l.Key
		if l.Landed.IsZero() {
			r := prow{text: text + pad(l.State, 10) + pal.dim.Render(" "+l.Path), key: key}
			if strings.HasPrefix(l.State, "exited") || l.State == "gone" {
				r.does = []pdo{revive(host, "", l.Key)}
			}
			running = append(running, r)
			continue
		}
		how := "ended "
		if !l.Killed.IsZero() {
			how = "killed "
		}
		how += l.Landed.Local().Format("Jan 2 3:04PM")
		if l.Recovered {
			how += ", work bundled"
		}
		ended = append(ended, prow{text: text + pad(how, 30) + pal.dim.Render(" "+l.Path), key: key, does: []pdo{revive(host, "", l.Key)}})
	}
	rows := []prow{{text: pal.bold.Render("running and exited")}}
	if len(running) == 0 {
		rows = append(rows, prow{text: pal.dim.Render("   none; + or n starts one")})
	}
	rows = append(rows, running...)
	var goals []prow
	for _, g := range m.killedGoals() {
		host, id, ask := g[0], g[1], g[2]
		goals = append(goals, prow{text: pad(m.hostTag(host), 6) + " " + pad(id, 10) + " " + oneLine(ask), key: "panel:launch:" + qual(host, id), does: []pdo{revive(host, host, id)}})
	}
	if len(goals) > 0 {
		rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("killed goals") + pal.dim.Render("  enter reopens it and revives every session its kill ended")})
		rows = append(rows, goals...)
	}
	rows = append(rows, prow{text: ""}, prow{text: pal.bold.Render("ended, recoverable") + pal.dim.Render("  enter recreates the worktree, puts the work back and resumes the conversation")})
	if len(ended) == 0 {
		rows = append(rows, prow{text: pal.dim.Render("   none")})
	}
	return append(rows, ended...), nil
}

// killedGoals are the goals ym kill ended on every host, as host, id and ask.
func (m Model) killedGoals() [][3]string {
	var out [][3]string
	for _, g := range m.board.Org.Goals {
		if g.State == core.GoalKilled {
			out = append(out, [3]string{"", g.ID, g.Ask})
		}
	}
	for _, r := range m.board.Remotes {
		for _, g := range r.Goals {
			if g.State == core.GoalKilled {
				out = append(out, [3]string{r.Host, g.ID, g.Ask})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0]+out[i][1] < out[j][0]+out[j][1] })
	return out
}
