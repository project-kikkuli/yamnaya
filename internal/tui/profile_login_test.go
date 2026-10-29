package tui

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestContextualProfileRegistrationAuthenticationAndExplicitSwitch(t *testing.T) {
	for _, host := range []string{"", box} {
		for _, kind := range []string{"session", "dormant", "goal", "chief"} {
			t.Run(host+"/"+kind, func(t *testing.T) {
				m, calls, sw := panelBoard(t)
				h := "claude"
				if host != "" {
					h = "codex"
				}
				s := core.Live{Session: core.Session{ID: "conversation", Harness: h, Profile: "default", ConfigRoot: "/native", Cwd: "/work", Status: core.Waiting}, Remote: host}
				r := row{session: &s}
				if kind == "dormant" {
					r = row{dormant: &core.Dormant{Key: qual(host, "launch"), Session: s.ID, Harness: h, Profile: s.Profile, Cwd: s.Cwd}}
				}
				if kind == "goal" || kind == "chief" {
					n := core.Node{Manager: core.Manager{ID: "mgr", Session: s.ID, Harness: h, Host: host, Profile: s.Profile}}
					if kind == "goal" {
						n.Goal = &core.Goal{ID: "goal"}
					}
					r = row{node: &n}
				}
				next, _ := m.openPanel(panelProfiles, r)
				m = next.(Model)
				original := *m.pn.target
				rows, _ := m.profileRows()
				if rows[m.pn.cur].key != profileRowKey(host, h, "default") {
					t.Fatalf("wrong initial login %v", rows[m.pn.cur])
				}
				m = press(t, m, append(append([]string{"a"}, typed("New Login")...), "enter", "enter")...)
				if len(*calls) != 2 || (*calls)[0].host != host || (*calls)[0].args[0] != "profiles" || (*calls)[0].args[1] != "add" || (*calls)[0].args[2] != h || (*calls)[0].args[3] != "new-login" || !(*calls)[1].tty {
					t.Fatalf("flow commands: %+v", *calls)
				}
				wantLogin := []string{"profiles", "login", h, "new-login"}
				if host != "" {
					wantLogin = append(wantLogin, "--device-auth")
				}
				if !slices.Equal((*calls)[1].args, wantLogin) {
					t.Fatalf("native args: %q", (*calls)[1].args)
				}
				if len(sw.switched) != 0 {
					t.Fatal("registered login switched implicitly")
				}
				b := m.board
				b.Profiles = append(slices.Clone(b.Profiles), core.LoginProfile{Harness: h, ID: "new-login", Label: "same local name"})
				b.Remotes = slices.Clone(b.Remotes)
				b.Remotes[0].Profiles = append([]core.LoginProfile{{Harness: "claude", ID: "new-login", Label: "wrong harness"}}, b.Remotes[0].Profiles...)
				next, _ = m.Update(boardMsg{board: b})
				m = next.(Model)
				rows, _ = m.profileRows()
				if rows[m.pn.cur].key != profileRowKey(host, h, "new-login") || !reflect.DeepEqual(*m.pn.target, original) {
					t.Fatalf("refresh drifted login/target: %s %+v", rows[m.pn.cur].key, m.pn.target)
				}
				next, _ = m.Update(profileLoginMsg{host: host, harness: h, id: "new-login", epoch: m.pn.epoch, err: errors.New("cancelled")})
				m = next.(Model)
				if !strings.Contains(m.message, "i retries") || m.mode != modePanel {
					t.Fatalf("auth failure not recoverable: %q", m.message)
				}
				m = press(t, m, "i")
				next, _ = m.Update(profileLoginMsg{host: host, harness: h, id: "new-login", epoch: m.pn.epoch})
				m = next.(Model)
				if len(sw.switched) != 0 || !strings.Contains(m.message, "enter switches only") {
					t.Fatalf("missing explicit switch guidance: %s", m.message)
				}
				m = press(t, m, "enter")
				if !slices.Equal(sw.switched, []string{original.ref + " new-login"}) {
					t.Fatalf("wrong conversation switched: %v", sw.switched)
				}
				for _, call := range *calls {
					if slices.Contains(call.args, "default") {
						t.Fatalf("changed launch defaults: %+v", call)
					}
				}
			})
		}
	}
}

func TestProfileRegistrationFailuresRetainRootAndTargetWithoutLocalFallback(t *testing.T) {
	for _, failure := range []string{"registration", "offline", "unknown"} {
		t.Run(failure, func(t *testing.T) {
			m, calls, _ := panelBoard(t)
			s := core.Live{Session: core.Session{ID: "far", Harness: "codex", Profile: "default"}, Remote: box}
			next, _ := m.openPanel(panelProfiles, row{session: &s})
			m = next.(Model)
			original := *m.pn.target
			run := m.pn.run
			if failure == "registration" {
				m.pn.run = func(string, string, ...string) (string, error) { return "", errors.New("root overlaps") }
			}
			if failure == "offline" {
				m.core.Remotes = &hostRemotes{remotes: m.core.Remotes.(*remotes), silent: errors.New("offline")}
			}
			if failure == "unknown" {
				m.core.Remotes = nil
			}
			m = press(t, m, append(append([]string{"a"}, typed("new")...), "enter", "enter")...)
			if m.mode != modeInput || m.pn.in == nil || m.pn.in.pick || string(m.input) != "/home/me/.codex-new" || !reflect.DeepEqual(*m.pn.target, original) {
				t.Fatalf("registration recovery lost: input=%q target=%+v message=%q", m.input, m.pn.target, m.message)
			}
			if len(*calls) != 0 {
				t.Fatalf("remote failure reached another command: %+v", *calls)
			}
			if failure == "registration" {
				m.pn.run = run
				m = press(t, m, "enter")
				if len(*calls) != 2 || !(*calls)[1].tty {
					t.Fatalf("retry failed: %+v", *calls)
				}
			}
		})
	}
}

// withSessions is a board reading these sessions: a board indexes its sessions once, so another list needs a board of its own.
func withSessions(b core.Board, sessions []core.Live) core.Board {
	return core.Board{At: b.At, Fleet: b.Fleet, Launched: b.Launched, Org: b.Org, Profiles: b.Profiles, Remotes: b.Remotes, Repos: b.Repos, Sessions: sessions}
}

func TestRepeatedNamedProfileSwitchRebindsExactConversation(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[fails], func(t *testing.T) {
			m, _, sw := panelBoard(t)
			for _, id := range []string{"one", "two", "three"} {
				m.board.Profiles = append(m.board.Profiles, core.LoginProfile{Harness: "claude", ID: id, Root: "/root/" + id})
			}
			ps := slices.Clone(m.board.Profiles)
			m.core.ProfileList = func() []core.LoginProfile { return ps }
			s := core.Live{Session: core.Session{ID: "same-id", Harness: "claude", Profile: "one", ConfigRoot: "/root/one"}}
			next, _ := m.openPanel(panelProfiles, row{session: &s})
			m = next.(Model)
			if fails {
				sw.err = errors.New("busy source")
			}
			m = press(t, toPanelRow(t, m, "claude/two"), "enter")
			if fails {
				if m.pn.target.profile != "one" || m.pn.target.root != "/root/one" {
					t.Fatalf("failed switch rebound: %+v", m.pn.target)
				}
				return
			}
			s.Profile, s.ConfigRoot = "two", "/root/two"
			b := withSessions(m.board, []core.Live{s, {Session: core.Session{ID: s.ID, Harness: "claude", Profile: "three", ConfigRoot: "/wrong"}, Remote: box}})
			next, _ = m.Update(boardMsg{board: b})
			m = next.(Model)
			if m.pn.target.profile != "two" || m.pn.target.root != "/root/two" || !strings.Contains(ansi.Strip(m.panelView().String()), "current: claude/two") {
				t.Fatalf("stale target: %+v", m.pn.target)
			}
			m = press(t, toPanelRow(t, m, "claude/three"), "enter")
			if !slices.Equal(sw.switched, []string{"claude:one@same-id two", "claude:two@same-id three"}) {
				t.Fatalf("stale reference: %q", sw.switched)
			}
		})
	}
}

func TestStoppedLaunchesOpenContextualProfiles(t *testing.T) {
	for _, state := range []string{"exited 1", "sigterm", "sigkill", "gone", "running", "unknown"} {
		t.Run(state, func(t *testing.T) {
			m, _, _ := panelBoard(t)
			m.board.Launched = []core.Launched{{Key: qual(box, "stopped"), Session: "conversation", Harness: "codex", Profile: "far", State: state}}
			m = press(t, m, "E")
			rows, _ := m.panelRows()
			for i, r := range rows {
				if strings.Contains(r.text, "stopped") {
					m.pn.cur = i
					break
				}
			}
			m = press(t, m, "B")
			stopped := state != "running" && state != "unknown"
			if (m.pn.kind == panelProfiles) != stopped {
				t.Fatalf("state %s panel %v", state, m.pn.kind)
			}
			if stopped && (m.pn.host != box || m.pn.harness != "codex" || m.pn.target.ref != qual(box, "stopped")) {
				t.Fatalf("lost exited launch: %+v", m.pn.target)
			}
		})
	}
}

func TestAutoSwitchRebindsOnlyAuthoritativeHostAndConversation(t *testing.T) {
	m, _, _ := panelBoard(t)
	s := core.Live{Session: core.Session{ID: "same-id", Harness: "codex", Profile: "far", ConfigRoot: "/old"}, Remote: box}
	next, _ := m.openPanel(panelProfiles, row{session: &s})
	m = next.(Model)
	m = press(t, toPanelRow(t, m, "auto:"), "enter")
	if m.pn.target.profile != "far" {
		t.Fatal("auto destination guessed before snapshot")
	}
	m = toPanelRow(t, m, "claude/work")
	key := profileRowKey("", "claude", "work")
	b := withSessions(m.board, []core.Live{{Session: core.Session{ID: s.ID, Harness: "codex", Profile: "wrong", ConfigRoot: "/wrong"}}})
	next, _ = m.Update(boardMsg{board: b})
	m = next.(Model)
	if m.pn.target.profile != "far" {
		t.Fatal("auto rebound to another host")
	}
	s.Profile, s.ConfigRoot = "new", "/remote/new"
	b.Sessions = append(b.Sessions, s)
	next, _ = m.Update(boardMsg{board: b})
	m = next.(Model)
	rows, _ := m.panelRows()
	if m.pn.target.profile != "new" || m.pn.target.root != "/remote/new" || m.pn.target.ref != qual(box, "codex:new@same-id") || rows[m.pn.cur].key != key {
		t.Fatalf("auto target/focus drifted: %+v row %s", m.pn.target, rows[m.pn.cur].key)
	}
}

func TestLateRegistrationAndLoginCannotTakeOverAnotherPanel(t *testing.T) {
	m, calls, _ := panelBoard(t)
	m = press(t, m, append(append([]string{"B", "a"}, typed("pending")...), "enter")...)
	next, register := m.update(keyOf("enter"))
	m = next.(Model)
	oldEpoch := m.pn.epoch
	m = press(t, m, "esc", "B")
	rows, _ := m.panelRows()
	key := rows[m.pn.cur].key
	next, _ = m.Update(register())
	m = next.(Model)
	next, _ = m.Update(profileLoginMsg{host: box, harness: "codex", id: "far", epoch: oldEpoch})
	m = next.(Model)
	rows, _ = m.panelRows()
	if rows[m.pn.cur].key != key || len(*calls) != 1 || (*calls)[0].tty {
		t.Fatalf("old flow took over: calls=%+v cursor=%s", *calls, rows[m.pn.cur].key)
	}
}
