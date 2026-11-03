package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type switchingHarness struct{ session core.Session }

func (h switchingHarness) Name() string               { return h.session.Harness }
func (h switchingHarness) Owns(p proc.Proc) bool      { return p.Command() == h.Name() }
func (h switchingHarness) SessionID(proc.Proc) string { return h.session.ID }
func (h switchingHarness) Discover(ps []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range ps {
		s := h.session
		s.PID = p.PID
		s.TTY = p.TTY
		out = append(out, s)
	}
	return out
}
func (h switchingHarness) Resume(id string) []string              { return []string{h.Name(), "--resume", id} }
func (h switchingHarness) ParsePrompt(string) (core.Prompt, bool) { return core.Prompt{}, false }
func (h switchingHarness) Composer(string) (string, bool)         { return "", false }

func TestProfileSwitchResumesIdleAndExitedAndQueuesForBusySessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, harness := range []string{"claude", "codex"} {
		for _, status := range []string{"idle", "exited", "exited removed worktree", "quota waiting", "busy", "cancelled login", "unregistered login"} {
			t.Run(harness+"/"+status, func(t *testing.T) {
				state, cwd, bins := t.TempDir(), t.TempDir(), t.TempDir()
				t.Setenv("YAMNAYA_STATE_DIR", state)
				t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
				ran := filepath.Join(t.TempDir(), "resumed")
				script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" auth status \"*|*\" login status \"*) [ \"$YM_FIXTURE_LOGIN_FAILURE\" = 1 ] && exit 1; printf '%%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\nprintf '%%s\\n' \"$YAMNAYA_PROFILE\" \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_SECURESTORAGE_CONFIG_DIR\" \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$ANTHROPIC_API_KEY$CODEX_ACCESS_TOKEN\" \"$*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", shellQuote(ran), shellQuote(ran), shellQuote(ran))
				if err := os.WriteFile(filepath.Join(bins, harness), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ANTHROPIC_API_KEY", "synthetic-parent-key")
				t.Setenv("CODEX_ACCESS_TOKEN", "synthetic-parent-token")
				a := profile.Profile{Harness: harness, ID: "a", Root: t.TempDir(), Explicit: true}
				b := profile.Profile{Harness: harness, ID: "b", Root: t.TempDir(), Explicit: true}
				for _, p := range []profile.Profile{a, b} {
					if err := profile.Add(state, p); err != nil {
						t.Fatal(err)
					}
				}
				a.Root, b.Root = physicalProfileRoot(a.Root), physicalProfileRoot(b.Root)
				subscriptionIdentity(t, &a)
				if status != "unregistered login" {
					subscriptionIdentity(t, &b)
				}
				if status == "cancelled login" {
					t.Setenv("YM_FIXTURE_LOGIN_FAILURE", "1")
				}
				allowed := status == "idle" || strings.HasPrefix(status, "exited") || status == "quota waiting"

				rel, data := filepath.Join("projects", "p", profileConversation+".jsonl"), profileResponse(harness, time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)
				if harness == "codex" {
					rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
					data = profileMeta(profileConversation, "", 0) + data
				}
				putProfileFile(t, filepath.Join(a.Root, rel), data)
				l := testLauncher(t, state)
				st, err := core.OpenStore(filepath.Join(state, "ym.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				alive, signals := !strings.HasPrefix(status, "exited"), 0
				s := core.Session{Harness: harness, ID: profileConversation, Profile: "a", ConfigRoot: a.Root, Status: core.Idle, Cwd: cwd}
				if status == "busy" {
					s.Status = core.Busy
				}
				s.Account = a.Account
				mainAt := time.Now().Add(-time.Second)
				var native core.Harness = switchingHarness{session: s}
				if status == "quota waiting" {
					s.Status = core.Waiting
					native = quotaHarness{switchingHarness: switchingHarness{session: s}, at: mainAt}
					db, err := sql.Open("sqlite", filepath.Join(state, "ym.db"))
					if err != nil {
						t.Fatal(err)
					}
					event, _ := json.Marshal(core.Exhaustion{Harness: harness, Profile: a.ID, Account: a.Account, Session: s.ID, Reason: map[string]string{"claude": "subscription_rate_limit", "codex": "usage_limit_exceeded"}[harness], Seen: mainAt.Add(100 * time.Millisecond)})
					name := "7d"
					if harness == "codex" {
						name = "codex 7d"
					}
					window, _ := json.Marshal(core.Limit{Harness: harness, Profile: a.ID, Account: a.Account, Name: name, Used: 100, Seen: time.Now(), ResetsAt: time.Now().Add(time.Hour)})
					_, err = db.Exec(`CREATE TABLE ym_usage_exhaustions(body TEXT);CREATE TABLE ym_usage_limits(body TEXT)`)
					if err == nil {
						_, err = db.Exec(`INSERT INTO ym_usage_exhaustions VALUES(?)`, string(event))
					}
					if err == nil {
						_, err = db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(window))
					}
					db.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{native}, Hosts: []core.Host{quotaHost{text: "quota dialog"}},
					Procs: func() ([]proc.Proc, error) {
						if !alive {
							return nil, nil
						}
						return []proc.Proc{{PID: 4242, TTY: "/dev/ttys999", Args: []string{harness, "resume", profileConversation}}}, nil
					},
					Signal: func(int, syscall.Signal) error { signals++; alive = false; return nil },
				}
				l.rootOf = func(_ int, key string) string {
					if key == "CODEX_HOME" {
						return a.Root
					}
					if key == "YAMNAYA_LAUNCH_KEY" {
						return "conversation"
					}
					return ""
				}
				rec := launch.Record{Key: "conversation", Harness: harness, Session: profileConversation, NativePID: 4242, Dedicated: harness == "codex", Adopted: true, Worktree: launch.Worktree{Path: cwd}}
				rec.AttachedAt = mainAt.Add(-time.Second)
				setRecordProfile(&rec, a)
				if err = l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				chief, err := st.AddManager(core.Manager{Role: core.RoleChief, Name: "platform", Harness: harness, Profile: a.ID, ConfigRoot: a.Root})
				if err != nil {
					t.Fatal(err)
				}
				chief.Session = rec.Session
				if err = st.SaveManager(chief); err != nil {
					t.Fatal(err)
				}
				report, err := st.AddManager(core.Manager{Role: core.RoleManager, ParentID: chief.ID, Harness: harness, Profile: a.ID, ConfigRoot: a.Root})
				if err != nil {
					t.Fatal(err)
				}
				pending, err := st.AddMessage(rec.Session, "fixture pending instruction", core.MsgPending)
				if err != nil {
					t.Fatal(err)
				}
				if status == "exited removed worktree" {
					if err := os.RemoveAll(cwd); err != nil {
						t.Fatal(err)
					}
				}
				_, err = l.SwitchProfile(rec.Key, "b")
				if allowed && err != nil {
					t.Fatalf("switch failed: %v", err)
				}
				savedMessage, messageErr := st.Message(pending.Seq)
				if messageErr != nil || !reflect.DeepEqual(savedMessage, pending) {
					t.Fatalf("queued instruction changed: %#v %v", savedMessage, messageErr)
				}
				want := b
				if !allowed {
					want = a
				}
				savedChief, loadErr := st.Manager(chief.ID)
				if loadErr != nil || savedChief.Profile != want.ID || savedChief.ConfigRoot != want.Root || savedChief.Session != chief.Session || savedChief.Role != core.RoleChief {
					t.Fatalf("chief subscription or identity: %+v, %v", savedChief, loadErr)
				}
				savedReport, loadErr := st.Manager(report.ID)
				if loadErr != nil || savedReport.Profile != a.ID || savedReport.ConfigRoot != a.Root || savedReport.ParentID != chief.ID {
					t.Fatalf("chief switch changed its report: %+v, %v", savedReport, loadErr)
				}
				if !allowed {
					if (err == nil) != (status == "busy") || signals != 0 {
						t.Fatalf("busy action interrupted: %v signals=%d", err, signals)
					}
					if m, _ := loadMigration(state, harness, rec.Session); (m.State == "queued") != (status == "busy") {
						t.Fatalf("queued state %q for %s", m.State, status)
					}
					if _, err = os.Stat(filepath.Join(b.Root, rel)); !os.IsNotExist(err) {
						t.Fatal("busy switch published artifacts")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				got := strings.Split(resumed(t, ran), "\n")
				if len(got) != 7 || got[0] != "b" || got[5] != "" || !strings.Contains(got[6], profileConversation) {
					t.Fatalf("wrong resume/environment: %q", got)
				}
				if strings.Contains(got[6], "--chrome") != (harness == "claude") {
					t.Fatalf("switched %s session's Chrome flag: %q", harness, got[6])
				}
				if harness == "claude" {
					if data, err := os.ReadFile(filepath.Join(b.Root, ".claude.json")); err != nil || !strings.Contains(string(data), `"hasCompletedClaudeInChromeOnboarding": true`) {
						t.Fatalf("switched onto a root without Chrome onboarding seeded: %s %v", data, err)
					}
				}
				if harness == "claude" && (got[1] != b.Root || got[2] != b.Root) {
					t.Fatalf("Claude storage mismatch: %q", got)
				}
				if config, err := os.ReadFile(filepath.Join(b.Root, ".claude.json")); harness == "claude" && (err != nil || !strings.Contains(string(config), `"hasCompletedOnboarding": true`) || !strings.Contains(string(config), "fixture-user")) {
					t.Fatalf("switched onto a profile without onboarding done and its login kept: %s, %v", config, err)
				}
				if harness == "codex" && (got[3] != b.Root || got[4] != b.Root || !strings.Contains(got[6], "--no-daemon") || !strings.Contains(got[6], "sqlite_home=")) {
					t.Fatalf("Codex storage mismatch: %q", got)
				}
				saved, _ := l.records.Find(rec.Key)
				if saved.Profile != "b" || saved.Session != rec.Session || saved.ConfigRoot != b.Root {
					t.Fatalf("selection not persisted: %#v", saved)
				}
				copied, err := os.ReadFile(filepath.Join(b.Root, rel))
				if err != nil || string(copied) != data {
					t.Fatalf("history not preserved: %v", err)
				}
				if (signals > 0) != (!strings.HasPrefix(status, "exited")) {
					t.Fatalf("wrong shutdown signals: %d", signals)
				}
			})
		}
	}
}
