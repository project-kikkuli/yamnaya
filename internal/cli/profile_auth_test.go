package cli

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func subscriptionIdentity(t *testing.T, p *profile.Profile) {
	t.Helper()
	if p.Harness == "claude" {
		putProfileFile(t, filepath.Join(p.Root, ".claude.json"), `{"oauthAccount":{"accountUuid":"fixture-user","organizationUuid":"`+p.ID+`"}}`)
	} else {
		claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_user_id":"fixture-user","chatgpt_account_id":"` + p.ID + `"}}`))
		putProfileFile(t, filepath.Join(p.Root, "auth.json"), fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"id_token":%q,"account_id":%q}}`, "fixture."+claims+".fixture", p.ID))
	}
	p.Account = profile.Identity(*p)
}

func TestSwitchRequiresNativeSubscriptionAuthentication(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		for _, state := range []string{"authenticated", "cancelled", "missing identity", "API key", "unknown mode", "wrong account", "invalid status"} {
			t.Run(h+"/"+state, func(t *testing.T) {
				p := profile.Profile{Harness: h, ID: "destination", Root: t.TempDir(), Explicit: true}
				subscriptionIdentity(t, &p)
				status := `{"loggedIn":true,"authMethod":"claude.ai"}`
				exit := 0
				if state == "cancelled" {
					exit = 1
					status = "fixture-secret"
				}
				if state == "missing identity" {
					p.Account = ""
				}
				if state == "wrong account" {
					p.Account = "different"
				}
				if state == "invalid status" {
					status = "fixture-secret"
					if h == "codex" {
						exit = 1
					}
				}
				if state == "API key" || state == "unknown mode" {
					status = `{"loggedIn":true,"authMethod":"api_key"}`
					if h == "codex" {
						b, err := os.ReadFile(filepath.Join(p.Root, "auth.json"))
						if err != nil {
							t.Fatal(err)
						}
						mode := "apikey"
						if state == "unknown mode" {
							mode = ""
						}
						putProfileFile(t, filepath.Join(p.Root, "auth.json"), strings.ReplaceAll(string(b), `"chatgpt"`, fmt.Sprintf("%q", mode)))
					}
				}
				bin := filepath.Join(t.TempDir(), h)
				argv := filepath.Join(t.TempDir(), "argv")
				script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$CLAUDE_CONFIG_DIR\" \"$CODEX_HOME\" \"$*\" > %s\nprintf '%%s' %s\nexit %d\n", shellQuote(argv), shellQuote(status), exit)
				putProfileFile(t, bin, script)
				if err := os.Chmod(bin, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("YAMNAYA_"+strings.ToUpper(h)+"_BIN", bin)
				err := switchAuthentication(p, t.TempDir())
				if (err == nil) != (state == "authenticated") {
					t.Fatalf("auth=%v", err)
				}
				if err != nil && (strings.Contains(err.Error(), "fixture-secret") || !strings.Contains(err.Error(), "press i")) {
					t.Fatalf("unsafe/unactionable error %v", err)
				}
				if state == "authenticated" {
					b, err := os.ReadFile(argv)
					if err != nil || !strings.Contains(string(b), p.Root) {
						t.Fatalf("wrong native root: %q %v", b, err)
					}
				}
			})
		}
	}
}
