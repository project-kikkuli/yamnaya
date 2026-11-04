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
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestPreparingARecordFinishesOnboardingAndKeepsTheRestOfTheConfig(t *testing.T) {
	for _, where := range []struct {
		name string
		rec  func(root string) launch.Record
		file func(root, home string) string
	}{
		{"named profile", func(root string) launch.Record {
			return launch.Record{ConfigRoot: root, ConfigExplicit: true}
		}, func(root, _ string) string { return filepath.Join(root, ".claude.json") }},
		{"default profile with no CLAUDE_CONFIG_DIR", func(root string) launch.Record {
			return launch.Record{ConfigRoot: root}
		}, func(_, home string) string { return filepath.Join(home, ".claude.json") }},
		{"no profile in the record", func(string) launch.Record {
			return launch.Record{}
		}, func(root, _ string) string { return filepath.Join(root, ".claude.json") }},
	} {
		for _, c := range []struct {
			name   string
			config string
		}{
			{"absent file", ""},
			{"empty object", `{}`},
			{"theme null and fields missing", `{"theme":null,"numStartups":3,"oauthAccount":{"accountUuid":"u"}}`},
			{"reset by a login change", `{"hasCompletedOnboarding":false,"lastOnboardingVersion":"2.1.300","userID":"x"}`},
			{"already done", `{"hasCompletedOnboarding":true,"theme":"light"}`},
		} {
			t.Run(where.name+"/"+c.name, func(t *testing.T) {
				root, home, dir := t.TempDir(), t.TempDir(), t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("CLAUDE_CONFIG_DIR", root)
				path := where.file(root, home)
				if c.config != "" {
					if err := os.WriteFile(path, []byte(c.config), 0o640); err != nil {
						t.Fatal(err)
					}
				}
				rec := where.rec(root)
				rec.Harness, rec.Worktree = claude.Name, launch.Worktree{Path: dir}
				if err := prepareRecord(rec); err != nil {
					t.Fatal(err)
				}
				var got map[string]any
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(data, &got); err != nil {
					t.Fatal(err)
				}
				if got["hasCompletedOnboarding"] != true || got["hasCompletedClaudeInChromeOnboarding"] != true {
					t.Fatalf("onboarding not done: %s", data)
				}
				want := map[string]any{}
				if c.config != "" {
					if err := json.Unmarshal([]byte(c.config), &want); err != nil {
						t.Fatal(err)
					}
				}
				delete(got, "hasCompletedOnboarding")
				delete(got, "hasCompletedClaudeInChromeOnboarding")
				delete(got, "projects")
				delete(want, "hasCompletedOnboarding")
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("other keys changed: %v, want %v", got, want)
				}
				if info, err := os.Stat(path); c.config != "" && (err != nil || info.Mode().Perm() != 0o640) {
					t.Fatalf("mode %v, %v; want 0640", info.Mode().Perm(), err)
				}
				leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".claude.json.ym-*"))
				if len(leftovers) != 0 {
					t.Fatalf("temp files left: %v", leftovers)
				}
			})
		}
	}
}

func TestConfigsThatAreNotAnObjectNeverPanic(t *testing.T) {
	for _, c := range []struct {
		name, config string
		wantErr      bool
	}{
		{"literal null", `null`, false},
		{"null projects", `{"projects":null,"userID":"kept"}`, false},
		{"null project", `{"projects":{"/elsewhere":null},"userID":"kept"}`, false},
		{"truncated", `{"userID":`, true},
		{"array", `[]`, true},
		{"projects an array", `{"projects":[]}`, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, ".claude.json")
			if err := os.WriteFile(path, []byte(c.config), 0o600); err != nil {
				t.Fatal(err)
			}
			err := prepareRecord(launch.Record{Harness: claude.Name, ConfigRoot: root, ConfigExplicit: true, Worktree: launch.Worktree{Path: t.TempDir()}})
			if c.wantErr {
				if err == nil || !strings.Contains(err.Error(), path) {
					t.Fatalf("err = %v; want it to name %s", err, path)
				}
				if data, _ := os.ReadFile(path); string(data) != c.config {
					t.Fatalf("malformed config rewritten: %s", data)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile(path)
			if !strings.Contains(string(data), `"hasCompletedOnboarding": true`) || strings.Contains(c.config, "kept") != strings.Contains(string(data), "kept") {
				t.Fatalf("config %s", data)
			}
		})
	}
}

func TestPortingToAClaudeProfileFinishesItsOnboarding(t *testing.T) {
	for _, c := range []struct {
		name, to string
		done     bool
	}{
		{"onto_claude", "claude", true},
		{"onto_codex", "codex", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			state, bins := t.TempDir(), t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			script := "#!/bin/sh\nprintf '%s\\n' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'\n"
			for _, name := range []string{"claude", "codex", "cs", "python3"} {
				if err := os.WriteFile(filepath.Join(bins, name), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("YAMNAYA_CS_BIN", filepath.Join(bins, "cs"))
			from := map[string]string{"claude": "codex", "codex": "claude"}[c.to]
			a := profile.Profile{Harness: from, ID: "a", Root: t.TempDir(), Explicit: true}
			b := profile.Profile{Harness: c.to, ID: "b", Root: t.TempDir(), Explicit: true}
			for _, p := range []profile.Profile{a, b} {
				if err := profile.Add(state, p); err != nil {
					t.Fatal(err)
				}
			}
			a.Root, b.Root = physicalProfileRoot(a.Root), physicalProfileRoot(b.Root)
			subscriptionIdentity(t, &b)
			rel, data := filepath.Join("projects", "p", profileConversation+".jsonl"), profileResponse(from, time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)
			if from == "codex" {
				rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
				data = profileMeta(profileConversation, "", 0) + data
			}
			putProfileFile(t, filepath.Join(a.Root, rel), data)
			if c.to == "codex" {
				db, err := sql.Open("sqlite", filepath.Join(b.Root, "state_5.sqlite"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec(`CREATE TABLE threads(id, rollout_path, created_at, updated_at, source, model_provider, cwd, title, sandbox_policy, approval_mode, tokens_used, has_user_event, archived, cli_version, first_user_message, memory_mode, created_at_ms, updated_at_ms, thread_source, preview, recency_at, recency_at_ms, history_mode)`); err != nil {
					t.Fatal(err)
				}
			}
			l := testLauncher(t, state)
			plan, err := l.prepareProfilePort(core.Session{Harness: from, ID: profileConversation, Profile: "a", ConfigRoot: a.Root, Cwd: t.TempDir()}, c.to, "b")
			if err != nil {
				t.Fatal(err)
			}
			defer plan.Close()
			config, err := os.ReadFile(filepath.Join(b.Root, ".claude.json"))
			if c.done && (!strings.Contains(string(config), `"hasCompletedOnboarding": true`) || !strings.Contains(string(config), "fixture-user")) {
				t.Fatalf("config %s, %v; want onboarding done and the login kept", config, err)
			}
			if !c.done && !os.IsNotExist(err) {
				t.Fatalf("a codex port wrote Claude config: %s, %v", config, err)
			}
		})
	}
}

func TestPreparingARecordWritesTheLegacyConfigClaudeReadsFirst(t *testing.T) {
	const current, legacy = ".claude.json", ".config.json"
	for _, c := range []struct {
		name            string
		legacy, current string
		written         string
	}{
		{"legacy with content wins", `{"userID":"kept"}`, "", legacy},
		{"legacy with content wins over a current config", `{"userID":"kept"}`, `{"userID":"other"}`, legacy},
		{"empty legacy left by a sandbox leaves the current config in charge", "", `{"userID":"kept"}`, current},
		{"empty legacy alone gives way to a new current config", "", "", current},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, legacy), []byte(c.legacy), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.current != "" {
				if err := os.WriteFile(filepath.Join(root, current), []byte(c.current), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := prepareRecord(launch.Record{Harness: claude.Name, ConfigRoot: root, ConfigExplicit: true, Worktree: launch.Worktree{Path: t.TempDir()}}); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(root, c.written))
			if err != nil || !strings.Contains(string(data), `"hasCompletedOnboarding": true`) {
				t.Fatalf("%s: %s, %v", c.written, data, err)
			}
			if strings.Contains(c.legacy+c.current, "kept") && !strings.Contains(string(data), `"userID": "kept"`) {
				t.Fatalf("%s lost its other keys: %s", c.written, data)
			}
			other, before := current, c.current
			if c.written == current {
				other, before = legacy, c.legacy
			}
			data, err = os.ReadFile(filepath.Join(root, other))
			if err != nil && !os.IsNotExist(err) || string(data) != before {
				t.Fatalf("%s changed to %q, %v; want %q", other, data, err, before)
			}
			if c.written == legacy && c.current == "" && !os.IsNotExist(err) {
				t.Fatalf("wrote a config Claude does not read: %v", err)
			}
		})
	}
}

func TestPreparingACodexRecordLeavesClaudeConfigAlone(t *testing.T) {
	root := t.TempDir()
	rec := launch.Record{Harness: "codex", ConfigRoot: root, ConfigExplicit: true, Worktree: launch.Worktree{Path: t.TempDir()}}
	if err := prepareRecord(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("codex start wrote Claude config: %v", err)
	}
}

func TestEveryClaudeStartFindsOnboardingDone(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, path := range []string{"new launch", "revive", "unstall restart"} {
		t.Run(path, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			bins, config := filepath.Join(base, "bin"), filepath.Join(base, "config")
			for _, d := range []string{bins, config} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			configFile := filepath.Join(config, ".claude.json")
			snapshot := filepath.Join(base, "seen")
			reset := func() {
				t.Helper()
				os.Remove(snapshot)
				if err := os.WriteFile(configFile, []byte(`{"theme":null,"userID":"kept"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			script := fmt.Sprintf("#!/bin/sh\ncp %q %q.tmp && mv %q.tmp %q\necho %q\nexec sleep 60\n", configFile, snapshot, snapshot, snapshot, trustPrompt)
			if err := os.WriteFile(filepath.Join(bins, "claude"), []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			t.Setenv("YAMNAYA_STATE_DIR", base)
			reset()

			l := testLauncher(t, base)
			st, err := core.OpenStore(filepath.Join(base, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			l.core = &core.Core{Store: st, StateDir: base}
			started, err := l.Start(core.NewLaunch{Harness: claude.Name, Directory: filepath.Join(base, "work"), Slug: "task", Prompt: "fix it", Session: "11111111-1111-4111-8111-111111111111", Repo: filepath.Join(base, "work")})
			if err != nil {
				t.Fatal(err)
			}
			rec, _ := l.records.Find(started.Key)
			name := tmux.SessionName(rec.Key)
			switch path {
			case "new launch":
			case "revive":
				resumed(t, snapshot)
				if err := l.srv.Kill(name); err != nil {
					t.Fatal(err)
				}
				reset()
				if _, err := l.revive(rec); err != nil {
					t.Fatal(err)
				}
			case "unstall restart":
				waitScreen(t, l.srv, name, trustPrompt)
				reset()
				if err := l.unstall(time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			seen := resumed(t, snapshot)
			if !strings.Contains(seen, `"hasCompletedOnboarding": true`) || !strings.Contains(seen, `"userID": "kept"`) {
				t.Fatalf("claude started on %s", seen)
			}
		})
	}
}

func TestATypedMessageWaitsOutOnboarding(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name, screen, shows string
		held                bool
	}{
		{"theme picker", "../harness/claude/testdata/claude-onboarding-theme.txt", "Choose the text style", true},
		{"security notes", "../harness/claude/testdata/claude-onboarding-security.txt", "Security notes:", true},
		{"input box", "../harness/claude/testdata/claude-idle.txt", "❯", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			capture, err := filepath.Abs(c.screen)
			if err != nil {
				t.Fatal(err)
			}
			l := testLauncher(t, base)
			l.core = &core.Core{Harnesses: []core.Harness{claude.New()}}
			srv := l.srv
			if _, err := srv.Start(l.conf, "onboarding", base, []string{"sh", "-c", fmt.Sprintf("cat %q; exec sleep 60", capture)}); err != nil {
				t.Fatal(err)
			}
			pane, err := srv.Pane("onboarding")
			if err != nil {
				t.Fatal(err)
			}
			waitScreen(t, srv, "onboarding", c.shows)
			shown, live, err := l.pane(pane.ID, claude.Name).Prompt()
			if err != nil || !live || shown != c.held {
				t.Fatalf("Prompt = %v, %v, %v; want shown %v", shown, live, err, c.held)
			}
		})
	}
}
