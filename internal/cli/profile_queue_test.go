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

// turnHarness reports one session, on pid only, as whatever *s says now.
type turnHarness struct {
	switchingHarness
	s   *core.Session
	pid int
}

func (h turnHarness) Discover(ps []proc.Proc) []core.Session {
	var out []core.Session
	for _, p := range ps {
		if p.PID == h.pid {
			s := *h.s
			s.PID, s.TTY = p.PID, p.TTY
			out = append(out, s)
		}
	}
	return out
}

func TestAQueuedSwitchAppliesWhenTheSessionItselfEndsItsTurn(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	const reason = "default login exhausted: weekly limit, resets Nov 5 3am UTC"
	running := `{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_run","name":"Bash","input":{"command":"go test ./...","description":"Run the full suite","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_run","type":"tool_result","content":"Command running in background with ID: b1run."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"b1run"}}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_done","name":"Bash","input":{"command":"git fetch origin","run_in_background":true}}]}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_done","type":"tool_result","content":"Command running in background with ID: b2done."}]},"toolUseResult":{"stdout":"","backgroundTaskId":"b2done"}}
{"type":"user","message":{"content":[{"tool_use_id":"toolu_agent","type":"tool_result","content":"Async agent launched"}]},"toolUseResult":{"isAsync":true,"status":"async_launched","agentId":"a65be36e","description":"Slack sweep"}}
{"type":"queue-operation","operation":"enqueue","content":"<task-notification>\n<task-id>b2done</task-id>\n<status>completed</status>\n</task-notification>"}
`
	for _, c := range []struct {
		name, harness string
		external      bool
		tasks         []string
		blocked       string
	}{
		{name: "claude with background work", harness: "claude", tasks: []string{"background Bash b1run: Run the full suite", "background Agent a65be36e: Slack sweep"}},
		{name: "codex", harness: "codex"},
		{name: "codex shared lifecycle", harness: "codex", external: true, blocked: "external/shared Codex lifecycle pid 5555"},
	} {
		t.Run(c.name, func(t *testing.T) {
			state, cwd, bins := t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			for _, k := range []string{"ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CODEX_SQLITE_HOME"} {
				t.Setenv(k, "")
			}
			ran := filepath.Join(t.TempDir(), "resumed")
			script := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *\" auth status \"*|*\" login status \"*) printf '%%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'; exit 0;; esac\nprintf '%%s\\n' \"$YAMNAYA_PROFILE\" \"$*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", shellQuote(ran), shellQuote(ran), shellQuote(ran))
			putProfileFile(t, filepath.Join(bins, c.harness), script)
			if err := os.Chmod(filepath.Join(bins, c.harness), 0700); err != nil {
				t.Fatal(err)
			}
			sourceRoot := t.TempDir()
			t.Setenv(profile.EnvKey(c.harness), sourceRoot)
			work := profile.Profile{Harness: c.harness, ID: "claude-work", Root: t.TempDir(), Explicit: true}
			if err := profile.Add(state, work); err != nil {
				t.Fatal(err)
			}
			work.Root = physicalProfileRoot(work.Root)
			subscriptionIdentity(t, &work)
			ps, err := profile.Load(state)
			if err != nil {
				t.Fatal(err)
			}
			def, err := profile.Find(ps, c.harness, "default")
			if err != nil {
				t.Fatal(err)
			}
			subscriptionIdentity(t, &def)

			rel, data := filepath.Join("projects", "p", profileConversation+".jsonl"), profileResponse(c.harness, time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)+running
			if c.harness == "codex" {
				rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
				data = profileMeta(profileConversation, "", 0) + profileResponse(c.harness, time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)
			}
			putProfileFile(t, filepath.Join(def.Root, rel), data)

			st, err := core.OpenStore(filepath.Join(state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			session := core.Session{Harness: c.harness, ID: profileConversation, Profile: "default", Account: def.Account, ConfigRoot: def.Root, Status: core.Busy, Cwd: cwd}
			alive, signals := true, 0
			l := testLauncher(t, state)
			l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{turnHarness{switchingHarness: switchingHarness{session: session}, s: &session, pid: 4242}},
				Procs: func() ([]proc.Proc, error) {
					var out []proc.Proc
					if alive {
						out = append(out, proc.Proc{PID: 4242, TTY: "/dev/ttys999", Args: []string{c.harness, "resume", profileConversation}})
					}
					if c.external {
						out = append(out, proc.Proc{PID: 5555, Args: []string{c.harness, "app-server"}})
					}
					return out, nil
				},
				Signal: func(int, syscall.Signal) error { signals++; alive = false; return nil },
			}
			l.core.Launches = l
			l.rootOf = func(pid int, key string) string {
				switch {
				case key == "CODEX_HOME":
					return def.Root
				case key == "YAMNAYA_LAUNCH_KEY" && pid == 4242:
					return "conversation"
				}
				return ""
			}
			rec := launch.Record{Key: "conversation", Harness: c.harness, Session: profileConversation, NativePID: 4242, Dedicated: c.harness == "codex", Adopted: true, Worktree: launch.Worktree{Path: cwd}, AttachedAt: time.Now().Add(-time.Minute)}
			setRecordProfile(&rec, def)
			if err = l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			row := func() string {
				t.Helper()
				b, err := l.core.Board()
				if err != nil {
					t.Fatal(err)
				}
				s, ok := b.Session(profileConversation)
				if !ok {
					return "gone"
				}
				if s.Switching == nil {
					return profile.ID(s.Profile)
				}
				return s.Switching.String()
			}
			serve := func() {
				t.Helper()
				b, err := l.core.Board()
				if err != nil {
					t.Fatal(err)
				}
				if err = l.RecoverProfiles(b); err != nil {
					t.Fatal(err)
				}
			}

			msg, err := l.QueueProfileSwitch(rec.Key, "claude-work", reason)
			if err != nil || !strings.Contains(msg, "queued") || signals != 0 {
				t.Fatalf("busy switch = %q, %v, signals %d; want it queued without a signal", msg, err, signals)
			}
			if got := row(); got != "switch pending -> claude-work" {
				t.Fatalf("board row while busy = %q", got)
			}
			serve()
			if signals != 0 || row() != "switch pending -> claude-work" {
				t.Fatalf("ym serve applied the switch mid-turn: signals %d, row %q", signals, row())
			}

			session.Status = core.Idle
			session.Crew = []core.Member{{ID: "a65be36e", Name: "Slack sweep", Status: core.Busy}}
			serve()

			saved, _ := l.records.Find(rec.Key)
			if c.blocked != "" {
				if signals != 0 || saved.Profile != "default" {
					t.Fatalf("blocked switch stopped or rebound the session: signals %d, profile %q", signals, saved.Profile)
				}
				if got := row(); !strings.HasPrefix(got, "switch -> claude-work blocked: "+c.blocked) {
					t.Fatalf("board row = %q, want the blocker", got)
				}
				if m, _ := loadMigration(state, c.harness, profileConversation); m.State == "running" || m.Version == 0 {
					t.Fatalf("blocked switch was dropped or marked running: %+v", m)
				}
				if _, err := os.Stat(filepath.Join(work.Root, rel)); !os.IsNotExist(err) {
					t.Fatal("blocked switch published the conversation")
				}
				return
			}
			if signals == 0 {
				t.Fatal("the switch never stopped the session at its turn end")
			}
			if saved.Profile != "claude-work" || saved.Session != profileConversation || saved.ConfigRoot != work.Root {
				t.Fatalf("record after apply = %+v; want claude-work with the same conversation", saved)
			}
			got := strings.SplitN(resumed(t, ran), "\n", 2)
			if len(got) != 2 || got[0] != "claude-work" || !strings.Contains(got[1], profileConversation) {
				t.Fatalf("resume = %q; want the same conversation under claude-work", got)
			}
			note := "ym switched this conversation from profile default to profile claude-work. Reason: " + reason + "."
			if !strings.Contains(got[1], note) {
				t.Fatalf("resume note = %q; want %q", got[1], note)
			}
			for _, task := range c.tasks {
				if !strings.Contains(got[1], task) {
					t.Errorf("resume note misses %q: %q", task, got[1])
				}
			}
			if strings.Contains(got[1], "b2done") || (len(c.tasks) == 0) == strings.Contains(got[1], "background tasks") {
				t.Errorf("resume note lists the wrong background tasks: %q", got[1])
			}
			if copied, err := os.ReadFile(filepath.Join(work.Root, rel)); err != nil || string(copied) != data {
				t.Fatalf("conversation not carried to claude-work: %v", err)
			}
			if m, _ := loadMigration(state, c.harness, profileConversation); m.State != "running" || m.Blocked != "" {
				t.Fatalf("applied switch = %+v; want running", m)
			}
			if p := l.PendingSwitches(); len(p) != 0 {
				t.Fatalf("board still shows a pending switch: %+v", p)
			}
		})
	}
}
