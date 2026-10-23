package profile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func isolated(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "default-claude"))
	t.Setenv("CODEX_HOME", filepath.Join(dir, "default-codex"))
	t.Setenv("YAMNAYA_PROFILE", "")
	return filepath.Join(dir, "state")
}
func TestDefaultsAreStableInsideNamedChildren(t *testing.T) {
	for _, secure := range []string{"absent", "empty", "redirect"} {
		t.Run(secure, func(t *testing.T) {
			dir := isolated(t)
			if secure == "absent" {
				t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "x")
				os.Unsetenv("CLAUDE_SECURESTORAGE_CONFIG_DIR")
			} else if secure == "empty" {
				t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "")
			} else {
				t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", filepath.Join(t.TempDir(), "secure"))
			}
			original, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			named := Profile{Harness: "claude", ID: "work", Root: filepath.Join(t.TempDir(), "root")}
			if err = Add(dir, named); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YAMNAYA_PROFILE", "work")
			t.Setenv("CLAUDE_CONFIG_DIR", named.Root)
			t.Setenv("CODEX_HOME", named.Root)
			t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", named.Root)
			loaded, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range original {
				q, err := Find(loaded, p.Harness, "")
				if err != nil || q.Root != p.Root || q.Explicit != p.Explicit || q.SecureStorage != p.SecureStorage || q.SecureStorageSet != p.SecureStorageSet {
					t.Fatalf("default changed: %#v %#v %v", p, q, err)
				}
			}
		})
	}
}
func TestArbitraryProfilesAndPhysicalRootIsolation(t *testing.T) {
	dir := isolated(t)
	for _, h := range []string{"claude", "codex"} {
		for _, id := range []string{"one", "two", "three", "four"} {
			root := filepath.Join(t.TempDir(), id)
			if err := Add(dir, Profile{Harness: h, ID: id, Label: "same label", Root: root}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ps, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 10 {
		t.Fatalf("profiles=%d", len(ps))
	}
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err = os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err = Add(dir, Profile{Harness: "claude", ID: "physical", Root: target}); err != nil {
		t.Fatal(err)
	}
	if err = Add(dir, Profile{Harness: "codex", ID: "alias", Root: link}); err == nil {
		t.Fatal("symlink credential alias accepted as independent root")
	}
}

func TestRegistrationCanonicalizesRootsThatDoNotExistYet(t *testing.T) {
	dir := isolated(t)
	parent := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(parent, link); err != nil {
		t.Fatal(err)
	}
	if err := Add(dir, Profile{Harness: "codex", ID: "fresh", Root: filepath.Join(link, "missing", "root")}); err != nil {
		t.Fatal(err)
	}
	ps, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	p, err := Find(ps, "codex", "fresh")
	if err != nil || p.Root != filepath.Join(canonical(parent), "missing", "root") {
		t.Fatalf("root is not canonical: %#v %v", p, err)
	}
}
func TestNamedEnvironmentSelectsNativeCredentialsWithoutLeakingParent(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			p := Profile{Harness: h, ID: "p", Root: t.TempDir(), Explicit: true}
			dirty := []string{"PATH=" + os.Getenv("PATH"), "YAMNAYA_PROFILE=wrong", "CLAUDE_CONFIG_DIR=/wrong", "CLAUDE_SECURESTORAGE_CONFIG_DIR=", "CODEX_HOME=/wrong", "CODEX_SQLITE_HOME=/wrong"}
			for _, k := range subscriptionOverrides {
				dirty = append(dirty, k+"=fixture-secret")
			}
			args := p.Argv([]string{"env"})
			child := exec.Command(args[0], args[1:]...)
			child.Env = dirty
			b, err := child.Output()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "fixture-secret") {
				t.Fatal("parent auth escaped into child")
			}
			for _, env := range [][]string{strings.Split(string(b), "\n"), p.Environment(dirty)} {
				if !slices.Contains(env, EnvKey(h)+"="+p.Root) {
					t.Fatal("missing selected root")
				}
				if h == "claude" && !slices.Contains(env, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+p.Root) {
					t.Fatal("secure-storage override contamination")
				}
				if h == "codex" && !slices.Contains(env, "CODEX_SQLITE_HOME="+p.Root) {
					t.Fatal("sqlite redirect contamination")
				}
			}
		})
	}
}
func TestNativeIdentityIsPrincipalAndSelectedWorkspace(t *testing.T) {
	for _, h := range []string{"claude", "codex"} {
		t.Run(h, func(t *testing.T) {
			var identities []string
			for _, v := range []struct{ user, workspace, claim string }{{"u1", "w1", "old"}, {"u1", "w1", "other"}, {"u2", "w1", "w1"}, {"u1", "w2", "w1"}} {
				root := t.TempDir()
				p := Profile{Harness: h, ID: "named", Label: "same", Root: root, Explicit: true}
				var doc any
				if h == "claude" {
					doc = map[string]any{"oauthAccount": map[string]string{"accountUuid": v.user, "organizationUuid": v.workspace, "emailAddress": "same@example.test"}}
				} else {
					claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_user_id": v.user, "chatgpt_account_id": v.claim}})
					token := "fixture." + base64.RawURLEncoding.EncodeToString(claims) + ".fixture"
					doc = map[string]any{"tokens": map[string]string{"id_token": token, "account_id": v.workspace}}
				}
				b, _ := json.Marshal(doc)
				name := ".claude.json"
				if h == "codex" {
					name = "auth.json"
				}
				if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
					t.Fatal(err)
				}
				identities = append(identities, Identity(p))
			}
			if identities[0] == "" || identities[0] != identities[1] || identities[0] == identities[2] || identities[0] == identities[3] {
				t.Fatalf("identities: %v", identities)
			}
		})
	}
}

func TestDefaultAuthenticationRootSpellingAndSQLiteRedirectSurviveNamedChildren(t *testing.T) {
	dir := isolated(t)
	physical := t.TempDir()
	link := filepath.Join(t.TempDir(), "legacy-link")
	if err := os.Symlink(physical, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", link)
	sqlRoot := t.TempDir()
	t.Setenv("CODEX_SQLITE_HOME", sqlRoot)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	original, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_SQLITE_HOME", t.TempDir())
	t.Setenv("YAMNAYA_PROFILE", "named")
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"claude", "codex"} {
		p, err := Find(loaded, h, "default")
		if err != nil {
			t.Fatal(err)
		}
		env := p.Environment([]string{"CODEX_SQLITE_HOME=/contaminated", "CLAUDE_CONFIG_DIR=/contaminated"})
		if h == "claude" && !slices.Contains(env, "CLAUDE_CONFIG_DIR="+link) {
			t.Fatal("canonicalization changed the legacy Keychain namespace")
		}
		if h == "codex" && !slices.Contains(env, "CODEX_SQLITE_HOME="+sqlRoot) {
			t.Fatal("named child changed the default native state routing")
		}
		prior, _ := Find(original, h, "default")
		if p.Root != prior.Root || p.NativeRoot() != prior.NativeRoot() {
			t.Fatal("default selection changed")
		}
	}
}

func TestClaudeDefaultConstraintsDoNotDisableNamedProfiles(t *testing.T) {
	for _, tc := range []struct {
		name, value      string
		set, unsupported bool
	}{{"absent", "", false, false}, {"empty", "", true, true}, {"relative", "relative-root", true, true}, {"absolute", "/absolute-root", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolated(t)
			if tc.set {
				t.Setenv("CLAUDE_CONFIG_DIR", tc.value)
			} else {
				os.Unsetenv("CLAUDE_CONFIG_DIR")
			}
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			ps, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			p, err := Find(ps, "claude", "default")
			if err != nil {
				t.Fatal(err)
			}
			if (p.DefaultConstraint() != nil) != tc.unsupported {
				t.Fatalf("constraint=%v", p.DefaultConstraint())
			}
			named := Profile{Harness: "claude", ID: "valid", Root: t.TempDir(), Explicit: true}
			if err = Add(dir, named); err != nil {
				t.Fatal(err)
			}
			if err = named.ValidateSubscription(""); err != nil {
				t.Fatalf("unsupported default disabled a named root: %v", err)
			}
			codex, _ := Find(ps, "codex", "default")
			if err = codex.DefaultConstraint(); err != nil {
				t.Fatal("Claude default disabled Codex")
			}
		})
	}
}
