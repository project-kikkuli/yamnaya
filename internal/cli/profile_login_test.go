package cli

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestRemoteDeviceAuthLoginProcess(t *testing.T) {
	if os.Getenv("YM_TEST_REMOTE_LOGIN") == "" {
		return
	}
	for i, arg := range os.Args {
		if arg == "--" {
			cmd := NewRoot()
			cmd.SetArgs(os.Args[i+1:])
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("missing remote command arguments")
}

func TestRemoteDeviceLoginReachesNativeOnOwningHost(t *testing.T) {
	local, remote, home, bins := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", local)
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	t.Setenv("YM_TEST_REMOTE_LOGIN", "1")
	p := profile.Profile{Harness: "codex", ID: "personal", Root: t.TempDir()}
	if err := profile.Add(remote, p); err != nil {
		t.Fatal(err)
	}
	p.Root = physicalProfileRoot(p.Root)
	result := filepath.Join(t.TempDir(), "native-arguments")
	files := map[string]string{
		filepath.Join(home, ".local", "bin", "ym"): "#!/bin/sh\nunset YM_CLI_TEST_CHILD\nexport YAMNAYA_STATE_DIR=" + shellQuote(remote) + "\nexec " + shellQuote(os.Args[0]) + " -test.run=^TestRemoteDeviceAuthLoginProcess$ -- \"$@\"\n",
		filepath.Join(bins, "codex"):               "#!/bin/sh\nprintf '%s\\n' \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$@\" > " + shellQuote(result) + "\n",
		filepath.Join(bins, "ssh"):                 "#!/bin/sh\nif [ \"$1\" = -t ]; then shift; fi\nshift\nHOME=" + shellQuote(home) + " exec sh -c \"$1\"\n",
	}
	for path, body := range files {
		putProfileFile(t, path, body)
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("YAMNAYA_SSH_BIN", filepath.Join(bins, "ssh"))
	cmd := NewRoot()
	cmd.SetArgs([]string{"remote", "fixture-host", "profiles", "login", "codex", "personal", "--device-auth"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	want := p.Root + "\n" + p.Root + "\n-c\nsqlite_home=" + strconv.Quote(p.Root) + "\nlogin\n--device-auth\n"
	if string(got) != want {
		t.Fatalf("remote native argv/root %q, want %q", got, want)
	}
}

func TestClaudeRejectsDeviceAuthorizationBeforeChangingState(t *testing.T) {
	state := filepath.Join(t.TempDir(), "unused")
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	cmd := NewRoot()
	cmd.SetArgs([]string{"profiles", "login", "claude", "personal", "--device-auth"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "only for Codex") {
		t.Fatalf("device-auth for Claude: %v", err)
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("rejected login altered state: %v", err)
	}
}

func TestNativeLoginPreservesPriorPayerAndLeavesLoginIntervalUnknown(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%v", h, fail), func(t *testing.T) {
				state, bins := t.TempDir(), t.TempDir()
				t.Setenv("YAMNAYA_STATE_DIR", state)
				t.Setenv("YAMNAYA_NO_SERVE", "1")
				t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
				p := profile.Profile{Harness: h, ID: "personal", Root: t.TempDir()}
				if err := profile.Add(state, p); err != nil {
					t.Fatal(err)
				}
				p.Root = physicalProfileRoot(p.Root)
				authPath := filepath.Join(p.Root, ".claude.json")
				auth := func(user string) string {
					return fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"organizationUuid":"workspace"}}`, user)
				}
				if h == "codex" {
					authPath = filepath.Join(p.Root, "auth.json")
					auth = func(user string) string {
						claims := fmt.Sprintf(`{"https://api.openai.com/auth":{"chatgpt_user_id":%q,"chatgpt_account_id":"workspace"}}`, user)
						return fmt.Sprintf(`{"tokens":{"id_token":%q,"account_id":"workspace"}}`, "fixture."+base64.RawURLEncoding.EncodeToString([]byte(claims))+".fixture")
					}
				}
				putProfileFile(t, authPath, auth("a"))
				accountA := profile.Identity(p)
				p.Account = accountA
				path := filepath.Join(p.Root, "projects", "p", profileConversation+".jsonl")
				now := time.Now()
				stamp := now.UTC().Format(time.RFC3339Nano)
				prefix := ""
				if h == "codex" {
					path = filepath.Join(p.Root, "sessions", "rollout-2026-10-23T10-00-00-"+profileConversation+".jsonl")
					prefix = profileMeta(profileConversation, "", 0)
				}
				putProfileFile(t, path, prefix+profileResponse(h, stamp, "first", 100, 20))
				m := profileMeter(t, state, []profile.Profile{p})
				defer m.Close()
				scanProfiles(t, m, now)
				in, out := 30, 5
				if h == "codex" {
					in, out = 130, 25
				}
				appendProfileFile(t, path, profileResponse(h, stamp, "before-login", in, out))
				in, out = 20, 4
				if h == "codex" {
					in, out = 150, 29
				}
				fixtureAuth, fixtureSuffix := filepath.Join(bins, "next-identity"), filepath.Join(bins, "during-login")
				putProfileFile(t, fixtureAuth, auth("b"))
				putProfileFile(t, fixtureSuffix, profileResponse(h, stamp, "during-login", in, out))
				body := "#!/bin/sh\ncat " + shellQuote(fixtureSuffix) + " >> " + shellQuote(path) + "\ncp " + shellQuote(fixtureAuth) + " " + shellQuote(authPath) + "\n"
				if fail {
					body += "exit 7\n"
				}
				putProfileFile(t, filepath.Join(bins, h), body)
				if err := os.Chmod(filepath.Join(bins, h), 0700); err != nil {
					t.Fatal(err)
				}
				cmd := NewRoot()
				cmd.SetArgs([]string{"profiles", "login", h, p.ID})
				err := cmd.Execute()
				if (err != nil) != fail {
					t.Fatalf("native login error %v, fail=%v", err, fail)
				}
				p.Account = profile.Identity(p)
				m.Profiles = []profile.Profile{p}
				in, out = 11, 3
				if h == "codex" {
					in, out = 161, 32
				}
				appendProfileFile(t, path, profileResponse(h, stamp, "after-login", in, out))
				check := func(f core.Fleet) {
					t.Helper()
					s := f.Sessions[profile.SessionKey(h, p.ID, profileConversation)]
					for account, want := range map[string]core.Tokens{accountA: {Input: 130, Output: 25}, "": {Input: 20, Output: 4}, p.Account: {Input: 11, Output: 3}} {
						if s.ByAccount[account].Tokens != want {
							t.Errorf("payer %q got %+v want %+v", account, s.ByAccount[account].Tokens, want)
						}
					}
					if f.Today.Tokens != (core.Tokens{Input: 161, Output: 32}) {
						t.Errorf("login lost charges: %+v", f.Today.Tokens)
					}
				}
				check(scanProfiles(t, m, now))
				m.Close()
				for _, rebuild := range []bool{false, true} {
					if rebuild {
						for _, name := range []string{"ym.db", "ym.db-wal", "ym.db-shm"} {
							os.Remove(filepath.Join(state, name))
						}
					}
					m = profileMeter(t, state, []profile.Profile{p})
					check(scanProfiles(t, m, now))
					m.Close()
				}
			})
		}
	}
}

func TestCodexDeviceLoginRunsNativeDeviceFlowInSelectedRoot(t *testing.T) {
	state, bins := t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	p := profile.Profile{Harness: "codex", ID: "personal", Root: t.TempDir()}
	if err := profile.Add(state, p); err != nil {
		t.Fatal(err)
	}
	p.Root = physicalProfileRoot(p.Root)
	putProfileFile(t, filepath.Join(bins, "codex"), "#!/bin/sh\nprintf '%s\\n' \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$@\"\n")
	if err := os.Chmod(filepath.Join(bins, "codex"), 0700); err != nil {
		t.Fatal(err)
	}
	cmd := NewRoot()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"profiles", "login", "codex", "personal", "--device-auth"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	want := p.Root + "\n" + p.Root + "\n-c\nsqlite_home=" + strconv.Quote(p.Root) + "\nlogin\n--device-auth\n"
	if out.String() != want {
		t.Fatalf("native device argv/root %q, want %q", out.String(), want)
	}
}

func TestNativeLoginUsesNamedIsolationAndPreservesLegacyDefaultRedirects(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			state, bins, legacy, redirect := t.TempDir(), t.TempDir(), t.TempDir(), t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("YAMNAYA_NO_SERVE", "1")
			t.Setenv("YAMNAYA_PROFILE", "")
			t.Setenv(profile.EnvKey(h), legacy)
			t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
			t.Setenv("CODEX_SQLITE_HOME", redirect)
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			if err := os.WriteFile(filepath.Join(bins, h), []byte("#!/bin/sh\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\" \"${CLAUDE_SECURESTORAGE_CONFIG_DIR-unset}\" \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$*\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			p := profile.Profile{Harness: h, ID: "work", Root: t.TempDir()}
			putProfileFile(t, filepath.Join(p.Root, "config.toml"), "sqlite_home = \"/copied/redirect\"\n")
			if err := profile.Add(state, p); err != nil {
				t.Fatal(err)
			}
			p.Root = physicalProfileRoot(p.Root)
			for _, id := range []string{"work", "default"} {
				cmd := NewRoot()
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs([]string{"profiles", "login", h, id})
				if err := cmd.Execute(); err != nil {
					t.Fatal(err)
				}
				got := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
				if len(got) != 5 {
					t.Fatalf("unexpected login output %q", got)
				}
				if h == "claude" {
					if id == "work" && (got[0] != p.Root || got[1] != p.Root) {
						t.Fatalf("named secure storage not pinned: %q", got)
					}
					if id == "default" && (got[0] != legacy || got[1] != "") {
						t.Fatalf("default empty override changed: %q", got)
					}
				} else {
					if id == "work" && (got[2] != p.Root || got[3] != p.Root || !strings.Contains(got[4], "sqlite_home=")) {
						t.Fatalf("named sqlite routing not pinned: %q", got)
					}
					if id == "default" && (got[2] != legacy || got[3] != redirect || got[4] != "login") {
						t.Fatalf("default sqlite redirect changed: %q", got)
					}
				}
			}
		})
	}
}
