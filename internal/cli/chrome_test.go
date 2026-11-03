package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestClaudeSessionsAttachChromeAndCodexSessionsDoNot(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	type path struct {
		base, harness, ran string
		l                  launcher
		rec                launch.Record
	}
	// ran is what the fake agent ym started was given.
	ran := func(t *testing.T, p path) []string {
		return strings.Split(resumed(t, p.ran), "\n")
	}
	live := func(p path) core.Live {
		return core.Live{Session: core.Session{ID: p.rec.Session, Harness: p.harness, Cwd: p.rec.Worktree.Path}}
	}
	for _, c := range []struct {
		name       string
		launches   bool
		claudeOnly bool
		argv       func(*testing.T, path) []string
	}{
		{"start argv", false, false, func(t *testing.T, p path) []string {
			argv, err := startArgv(p.rec)
			if err != nil {
				t.Fatal(err)
			}
			return argv
		}},
		{"resume argv", false, false, func(t *testing.T, p path) []string {
			argv, err := resumeArgv(p.rec, "carry on")
			if err != nil {
				t.Fatal(err)
			}
			return argv
		}},
		{"hand resume after a failed switch", false, false, func(t *testing.T, p path) []string {
			return strings.Fields(profileResume(p.rec))
		}},
		{"resume command of a session with no terminal", false, false, func(t *testing.T, p path) []string {
			c := core.Core{Harnesses: []core.Harness{&profileHarness{name: p.harness, dir: p.base}}}
			ts := c.Targets(core.Board{}, p.rec.Session, p.harness)
			if len(ts) != 1 {
				t.Fatalf("targets %+v", ts)
			}
			return ts[0].Resume
		}},
		{"resume command under a named profile", false, false, func(t *testing.T, p path) []string {
			return (&profileHarness{name: p.harness, dir: p.base}).ResumeProfile(p.rec.Session, "default")
		}},
		{"worktree launch", true, false, func(t *testing.T, p path) []string {
			repo := filepath.Join(p.base, "repo")
			git(t, p.base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, p.base, "clone", "-q", filepath.Join(p.base, "origin.git"), "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			if _, err := p.l.Start(core.NewLaunch{Harness: p.harness, Repo: repo, Slug: "task", Prompt: "go"}); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"chief workspace launch", true, false, func(t *testing.T, p path) []string {
			if _, err := p.l.Start(core.NewLaunch{Harness: p.harness, Directory: filepath.Join(p.base, "chief"), Slug: "chief", Prompt: "go", Repo: filepath.Join(p.base, "reference")}); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"revive", true, false, func(t *testing.T, p path) []string {
			if _, err := p.l.Revive(p.rec.Key); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"revive before any conversation", true, false, func(t *testing.T, p path) []string {
			p.rec.Session = ""
			if err := p.l.records.Save(p.rec); err != nil {
				t.Fatal(err)
			}
			if _, err := p.l.Revive(p.rec.Key); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"goal resume", true, false, func(t *testing.T, p path) []string {
			if _, _, err := p.l.Resume(p.rec.Session, "you stopped", "start over"); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"goal start over when the transcript is gone", true, true, func(t *testing.T, p path) []string {
			if err := os.RemoveAll(filepath.Join(p.base, "config", "projects")); err != nil {
				t.Fatal(err)
			}
			if _, _, err := p.l.Resume(p.rec.Session, "you stopped", "start over"); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"bring onto ym's server", true, false, func(t *testing.T, p path) []string {
			if err := p.l.Bring(live(p), "come over"); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"relaunch", true, false, func(t *testing.T, p path) []string {
			if _, err := p.l.Relaunch(live(p), p.harness, p.rec.Session); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
		{"restart past a lost trust dialog", true, true, func(t *testing.T, p path) []string {
			p.rec.Started = time.Now()
			if err := p.l.records.Save(p.rec); err != nil {
				t.Fatal(err)
			}
			argv, err := startArgv(p.rec)
			if err != nil {
				t.Fatal(err)
			}
			name := tmux.SessionName(p.rec.Key)
			if _, err := p.l.srv.Start(p.l.conf, name, p.rec.Worktree.Path, argv); err != nil {
				t.Fatal(err)
			}
			waitScreen(t, p.l.srv, name, trustPrompt)
			resumed(t, p.ran)
			if err := os.Remove(p.ran); err != nil {
				t.Fatal(err)
			}
			if err := p.l.unstall(time.Now()); err != nil {
				t.Fatal(err)
			}
			return ran(t, p)
		}},
	} {
		for _, harness := range []string{"claude", "codex"} {
			if c.claudeOnly && harness != "claude" {
				continue
			}
			t.Run(harness+" "+c.name, func(t *testing.T) {
				base, _ := filepath.EvalSymlinks(t.TempDir())
				bins, config := filepath.Join(base, "bin"), filepath.Join(base, "config")
				wt := filepath.Join(base, "wt")
				for _, dir := range []string{bins, config, wt} {
					if err := os.MkdirAll(dir, 0o755); err != nil {
						t.Fatal(err)
					}
				}
				p := path{base: base, harness: harness, ran: filepath.Join(base, "ran"), l: testLauncher(t, base),
					rec: launch.Record{Key: "repo-wt-task", Harness: harness, Session: profileConversation, Prompt: "go", Started: time.Now(), Worktree: launch.Worktree{Path: wt}}}
				script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %[1]q.tmp && mv %[1]q.tmp %[1]q\necho %[2]q\nexec sleep 60\n", p.ran, trustPrompt)
				if err := os.WriteFile(filepath.Join(bins, harness), []byte(script), 0o755); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
				t.Setenv("YAMNAYA_STATE_DIR", base)
				t.Setenv("CLAUDE_CONFIG_DIR", config)
				t.Setenv("CODEX_HOME", config)
				rel := filepath.Join("projects", "p", profileConversation+".jsonl")
				data := profileResponse(harness, time.Now().UTC().Format(time.RFC3339Nano), "first", 100, 20)
				if harness == "codex" {
					rel = filepath.Join("sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
					data = profileMeta(profileConversation, "", 0) + data
				}
				putProfileFile(t, filepath.Join(config, rel), data)
				if err := p.l.records.Save(p.rec); err != nil {
					t.Fatal(err)
				}
				argv := c.argv(t, p)
				if len(argv) == 0 {
					t.Fatal("no command")
				}
				if got, want := slices.Contains(argv, "--chrome"), harness == "claude"; got != want {
					t.Fatalf("argv %q: --chrome %v, want %v", argv, got, want)
				}
				if c.launches && harness == "claude" {
					if data, err := os.ReadFile(filepath.Join(config, ".claude.json")); err != nil || !strings.Contains(string(data), `"hasCompletedClaudeInChromeOnboarding": true`) {
						t.Fatalf("started on a root without Chrome onboarding seeded: %s %v", data, err)
					}
				}
			})
		}
	}
}
