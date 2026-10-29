package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestNativeAddDelegatesSupportedTransportsAndScopes(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		for _, scope := range []string{"user", "project", "local"} {
			for _, transport := range []string{"http", "sse", "stdio"} {
				t.Run(h+"/"+scope+"/"+transport, func(t *testing.T) {
					a := Addition{Name: "sample", Scope: scope, Transport: transport, URL: "https://example.invalid/mcp"}
					if transport == "stdio" {
						a.URL = ""
						a.Command = []string{"/native/tool", "--flag", "literal $HOME"}
					}
					args, err := a.Args(h)
					unsupported := h == "codex" && (scope != "user" || transport == "sse")
					if unsupported {
						if err == nil {
							t.Fatal("unsupported scope/transport silently changed")
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(args[:2], []string{"mcp", "add"}) {
						t.Fatal(args)
					}
					calls := 0
					n := Native{Run: func(_ context.Context, target Target, got []string, _ io.Reader, _, _ io.Writer) error {
						calls++
						if target.Native.Root != target.Root || !reflect.DeepEqual(got, args) {
							t.Fatal("native target/argv changed")
						}
						return nil
					}}
					if err = n.Add(context.Background(), testTarget(t, h), a); err != nil || calls != 1 {
						t.Fatalf("%d %v", calls, err)
					}
				})
			}
		}
	}
}

func TestClaudeCheckClassifiesPrivateStderrAndNeverStoresNativeOutput(t *testing.T) {
	for _, tc := range []struct {
		message, health, auth string
		failed                bool
	}{
		{"Status: ✓ Connected", "connected", "unknown", false},
		{"Status: ⏸ Pending approval", "approval-required", "unknown", false},
		{"Status: Needs authentication", "authentication-required", "needs-login", false},
		{"unknown command get", "unsupported", "unknown", true},
		{"spawn /mac/only ENOENT", "executable-unavailable", "unknown", true},
		{"Failed to connect", "native-connection-failed", "unknown", true},
	} {
		t.Run(tc.health, func(t *testing.T) {
			target := testTarget(t, "claude")
			s := Server{Name: "notion", Scope: "user", Source: target.ConfigFile, Effective: true, Configuration: "configured"}
			native := Native{Run: func(_ context.Context, _ Target, args []string, _ io.Reader, out, stderr io.Writer) error {
				if !reflect.DeepEqual(args, []string{"mcp", "get", "notion"}) {
					t.Fatal(args)
				}
				w := out
				if tc.failed {
					w = stderr
				}
				fmt.Fprintln(w, tc.message+" secret-sentinel https://callback.invalid/?code=secret-sentinel")
				if tc.failed {
					return fmt.Errorf("secret-sentinel")
				}
				return nil
			}}
			o, err := native.Check(context.Background(), target, s)
			if err != nil || o.Health != tc.health || o.Auth != tc.auth {
				t.Fatalf("%+v %v", o, err)
			}
			state := t.TempDir()
			if err = SaveObservation(state, target, s, o); err != nil {
				t.Fatal(err)
			}
			b, _ := os.ReadFile(observationPath(state, target, s))
			if bytes.Contains(b, []byte("sentinel")) || bytes.Contains(b, []byte("callback")) {
				t.Fatal("stored native secrets")
			}
		})
	}
}

func TestCodexStatusKeepsAuthenticationIndependentFromRuntime(t *testing.T) {
	for _, tc := range []struct {
		auth, runtime        string
		failure              bool
		wantAuth, wantHealth string
	}{
		{"oAuth", "connected", false, "oauth", "connected"},
		{"oAuth", "failed", true, "oauth", "native-discovery-failed"},
		{"notLoggedIn", "authenticationRequired", false, "needs-login", "authentication-required"},
		{"bearerToken", "", false, "bearer-token", "unknown"},
		{"unsupported", "disabled", false, "unsupported", "disabled"},
	} {
		data := map[string]any{"name": "notion", "authStatus": tc.auth, "runtimeStatus": tc.runtime}
		if tc.failure {
			data["toolsError"] = "secret-sentinel"
		}
		response, _ := json.Marshal(map[string]any{"id": 2, "result": map[string]any{"data": []any{data}}})
		input := &bytes.Buffer{}
		output := strings.NewReader("{\"id\":1,\"result\":{}}\n" + string(response) + "\n")
		o, err := codexStatus(input, output, "notion", Observation{Auth: "unknown", Health: "unknown", At: time.Now()})
		if err != nil || o.Auth != tc.wantAuth || o.Health != tc.wantHealth {
			t.Fatalf("%+v %v", o, err)
		}
		if !strings.Contains(input.String(), `"serverName":"notion"`) || strings.Contains(input.String(), "thread/start") {
			t.Fatal("check did not stay targeted")
		}
	}
}

func TestNativeAuthUsesSelectedEnvironmentAndOnlyLoginGetsNoBrowser(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		for _, logout := range []bool{false, true} {
			target := testTarget(t, h)
			var got []string
			n := Native{Run: func(_ context.Context, tg Target, args []string, _ io.Reader, _, _ io.Writer) error {
				got = args
				if tg.Root != target.Root {
					t.Fatal("wrong root")
				}
				return nil
			}}
			if err := n.Auth(context.Background(), target, Server{Name: "notion", Effective: true}, logout, true, strings.NewReader(""), io.Discard, io.Discard); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Join(got, " "), "--no-browser") == logout {
				t.Fatal(got)
			}
		}
	}
}

func TestNativeChildActuallyReceivesIsolatedRootAndCwd(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		target := testTarget(t, h)
		executable := filepath.Join(t.TempDir(), "native")
		put(t, executable, "#!/bin/sh\nprintf '%s\\n' \"$CLAUDE_CONFIG_DIR\" \"$CLAUDE_SECURESTORAGE_CONFIG_DIR\" \"$CODEX_HOME\" \"$CODEX_SQLITE_HOME\" \"$PWD\"\n")
		if err := os.Chmod(executable, 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CLAUDE_CONFIG_DIR", "/contaminated")
		t.Setenv("CODEX_HOME", "/contaminated")
		out := &bytes.Buffer{}
		if err := (Native{Binary: executable}).run(context.Background(), target, nil, nil, out, io.Discard); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		offset := 0
		if h == "codex" {
			offset = 2
		}
		if lines[offset] != target.Root || lines[offset+1] != target.Root || lines[4] != target.Cwd {
			t.Fatalf("native child target changed: %q", lines)
		}
	}
}
