package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
		out = append(out, s)
	}
	return out
}
func (h switchingHarness) Resume(id string) []string              { return []string{h.Name(), "--resume", id} }
func (h switchingHarness) ParsePrompt(string) (core.Prompt, bool) { return core.Prompt{}, false }
func (h switchingHarness) Composer(string) (string, bool)         { return "", false }

func TestProfileSwitchResumesIdleAndExitedButNeverInterruptsBusySessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, harness := range []string{"claude", "codex"} {
		for _, status := range []string{"idle", "exited", "busy"} {
			t.Run(harness+"/"+status, func(t *testing.T) {
				state, cwd, bins := t.TempDir(), t.TempDir(), t.TempDir()
				t.Setenv("YAMNAYA_STATE_DIR", state)
				t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
				ran := filepath.Join(t.TempDir(), "resumed")
				script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$YAMNAYA_PROFILE\" \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_SECURESTORAGE_CONFIG_DIR\" \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$ANTHROPIC_API_KEY$CODEX_ACCESS_TOKEN\" \"$*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", shellQuote(ran), shellQuote(ran), shellQuote(ran))
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
				alive, signals := status != "exited", 0
				s := core.Session{Harness: harness, ID: profileConversation, Profile: "a", ConfigRoot: a.Root, Status: core.Idle, Cwd: cwd}
				if status == "busy" {
					s.Status = core.Busy
				}
				l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{switchingHarness{s}},
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
				_, err = l.SwitchProfile(rec.Key, "b")
				want := b
				if status == "busy" {
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
				if status == "busy" {
					if err == nil || signals != 0 {
						t.Fatalf("busy action interrupted: %v signals=%d", err, signals)
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
				if harness == "claude" && (got[1] != b.Root || got[2] != b.Root) {
					t.Fatalf("Claude storage mismatch: %q", got)
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
				if (signals > 0) != (status == "idle") {
					t.Fatalf("wrong shutdown signals: %d", signals)
				}
			})
		}
	}
}
