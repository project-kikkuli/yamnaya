package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestChiefWorkspaceLaunchAndLegacyConversationMigration(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		t.Run(harness, func(t *testing.T) {
			base := t.TempDir()
			bins := filepath.Join(base, "bin")
			if err := os.MkdirAll(bins, 0755); err != nil {
				t.Fatal(err)
			}
			ran := filepath.Join(base, "ran")
			events := filepath.Join(base, "native-events")
			t.Setenv("YM_CODEX_FIXTURE_EVENTS", events)
			script := "#!/bin/sh\nprintf '%s\\n' \"$PWD\" \"$@\" > '" + ran + "'\nexec sleep 60\n"
			if err := os.WriteFile(filepath.Join(bins, harness), []byte(script), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			l := testLauncher(t, base)
			st, err := core.OpenStore(filepath.Join(base, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			l.core = &core.Core{Store: st, StateDir: base}
			chief, err := st.AddManager(core.Manager{Role: core.RoleChief, Name: "platform", Harness: harness, Repo: filepath.Join(base, "reference")})
			if err != nil {
				t.Fatal(err)
			}
			workspace := filepath.Join(base, "chiefs", chief.ID, "workspace")
			started, err := l.Start(core.NewLaunch{Harness: harness, Directory: workspace, Slug: chief.ID + "-platform", Prompt: "coordinate", Session: "11111111-1111-4111-8111-111111111111", Repo: chief.Repo})
			if err != nil {
				t.Fatal(err)
			}
			rec, _ := l.records.Find(started.Key)
			if rec.Worktree.Path != workspace || rec.Worktree.GitDir != "" || rec.Access != chief.Repo {
				t.Fatalf("chief created a worktree: %+v", rec)
			}
			if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
				t.Fatal(err)
			}
			legacy := filepath.Join(base, "legacy-repo")
			if err := os.MkdirAll(legacy, 0755); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(legacy, "plan.md")
			if err := os.WriteFile(sentinel, []byte("retained plan"), 0600); err != nil {
				t.Fatal(err)
			}
			rec.Worktree = launch.Worktree{Path: legacy, GitDir: filepath.Join(legacy, ".git"), Branch: "legacy"}
			rec.Session = "11111111-1111-4111-8111-111111111111"
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			chief.Session = rec.Session
			if err := st.SaveManager(chief); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(ran); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			if err := os.Remove(events); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			args, _, err := l.restart(rec, func(r *launch.Record) ([]string, error) { return resumeArgv(*r, "carry on") })
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(args, " "), rec.Session) {
				t.Fatalf("conversation lost: %v", args)
			}
			for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				b, _ := os.ReadFile(ran)
				if strings.HasPrefix(string(b), workspace+"\n") && (harness == "claude" && strings.Contains(string(b), "carry on") || harness == "codex" && strings.Contains(string(b), "--remote\nunix://")) {
					break
				}
				if time.Now().After(end) {
					t.Fatalf("did not resume in workspace: %s", b)
				}
			}
			if harness == "codex" {
				native, err := os.ReadFile(ran)
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(native), "carry on") {
					t.Fatalf("recovery text became native frontend input: %s", native)
				}
				raw, err := os.ReadFile(events)
				if err != nil {
					t.Fatal(err)
				}
				resumed, activated := false, false
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					var request struct {
						Method string
						Params struct {
							ThreadID, Cwd, TurnTrigger string
							Input                      []json.RawMessage
							ToolOutput                 *struct{ Namespace, Name, Output string }
						}
					}
					if err = json.Unmarshal([]byte(line), &request); err != nil {
						t.Fatal(err)
					}
					if request.Method == "thread/resume" {
						resumed = request.Params.ThreadID == rec.Session && request.Params.Cwd == workspace
					}
					if request.Method == "turn/start" {
						if len(request.Params.Input) != 0 {
							t.Fatalf("workspace migration fabricated user input: %s", line)
						}
						if request.Params.ToolOutput != nil && strings.Contains(request.Params.ToolOutput.Output, "carry on") {
							activated = true
						}
					}
				}
				if !resumed || !activated {
					t.Fatalf("native migration lost conversation or explicit control input: %s", raw)
				}
			}
			got, _ := l.records.Find(rec.Key)
			org, _ := l.core.LoadOrg()
			if got.Session != rec.Session || got.Worktree.Path != workspace || got.PreviousWorktree.Path != legacy || org.Managers[chief.ID].Directory != workspace {
				t.Fatalf("migration lost identity: %+v", got)
			}
			if b, err := os.ReadFile(sentinel); err != nil || string(b) != "retained plan" {
				t.Fatal("legacy work was removed")
			}
		})
	}
}
