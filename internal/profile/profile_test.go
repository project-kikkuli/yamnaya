package profile

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
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

func TestLoadSeesEveryProfileFileChange(t *testing.T) {
	claudeDoc := func(user string) string {
		return `{"oauthAccount":{"accountUuid":"` + user + `","organizationUuid":"org"}}`
	}
	authDoc := func(user string) string {
		claims, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]string{"chatgpt_user_id": user, "chatgpt_account_id": "acct"}})
		return `{"tokens":{"id_token":"x.` + base64.RawURLEncoding.EncodeToString(claims) + `.y"}}`
	}
	account := func(harness, id string) func([]Profile) string {
		return func(ps []Profile) string {
			p, err := Find(ps, harness, id)
			if err != nil {
				return ""
			}
			return p.Account
		}
	}
	label := func(ps []Profile) string {
		p, err := Find(ps, "claude", "work")
		if err != nil {
			return ""
		}
		return p.Label
	}
	work := func(dir, lbl string) string {
		b, _ := json.Marshal([]Profile{{Harness: "claude", ID: "work", Label: lbl, Root: filepath.Join(filepath.Dir(dir), "work")}})
		return string(b)
	}
	cases := []struct {
		name         string
		file         func(dir string) string
		before       func(dir string) string
		after        func(dir string) string
		read         func([]Profile) string
		seenB, seenA string
	}{
		{
			name:   "native login",
			file:   func(dir string) string { return filepath.Join(filepath.Dir(dir), "work", ".claude.json") },
			before: func(string) string { return claudeDoc("aaaa") },
			after:  func(string) string { return claudeDoc("bbbb") },
			read:   account("claude", "work"),
			seenB:  "claude/aaaa/org", seenA: "claude/bbbb/org",
		},
		{
			name:   "codex login",
			file:   func(dir string) string { return filepath.Join(filepath.Dir(dir), "default-codex", "auth.json") },
			before: func(string) string { return authDoc("aaaa") },
			after:  func(string) string { return authDoc("bbbb") },
			read:   account("codex", "default"),
			seenB:  "codex/aaaa/acct", seenA: "codex/bbbb/acct",
		},
		{
			name:   "settings take a login away",
			file:   func(dir string) string { return filepath.Join(filepath.Dir(dir), "default-claude", "settings.json") },
			before: func(string) string { return `{"env":{"NOTHING_HERE":"xxxxxxxx"}}` },
			after:  func(string) string { return `{"env":{"ANTHROPIC_API_KEY":"xxxxx"}}` },
			read:   account("claude", "default"),
			seenB:  "claude/dflt/org", seenA: "",
		},
		{
			name:   "profile list",
			file:   path,
			before: func(dir string) string { return work(dir, "aaaa") },
			after:  func(dir string) string { return work(dir, "bbbb") },
			read:   label,
			seenB:  "aaaa", seenA: "bbbb",
		},
	}
	for _, c := range cases {
		for _, change := range []struct {
			name string
			old  time.Duration
		}{{"right after the write", 0}, {"after the file settled", time.Hour}} {
			t.Run(c.name+"/"+change.name, func(t *testing.T) {
				dir := isolated(t)
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				for file, doc := range map[string]string{
					filepath.Join(filepath.Dir(dir), "default-claude", ".claude.json"): claudeDoc("dflt"),
					path(dir): work(dir, "work"),
				} {
					if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				file := c.file(dir)
				if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, []byte(c.before(dir)), 0o600); err != nil {
					t.Fatal(err)
				}
				if change.old > 0 {
					at := time.Now().Add(-change.old)
					if err := os.Chtimes(file, at, at); err != nil {
						t.Fatal(err)
					}
				}
				for range 2 {
					ps, err := Load(dir)
					if err != nil {
						t.Fatal(err)
					}
					if got := c.read(ps); got != c.seenB {
						t.Fatalf("before: %q, want %q", got, c.seenB)
					}
				}
				if err := os.WriteFile(file, []byte(c.after(dir)), 0o600); err != nil {
					t.Fatal(err)
				}
				ps, err := Load(dir)
				if err != nil {
					t.Fatal(err)
				}
				if got := c.read(ps); got != c.seenA {
					t.Fatalf("after: %q, want %q", got, c.seenA)
				}
			})
		}
	}
}

func TestLoadCostDoesNotGrowWithAnUnchangedAccountFile(t *testing.T) {
	dir := isolated(t)
	root := filepath.Join(filepath.Dir(dir), "default-claude")
	for _, d := range []string{dir, root} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	file := filepath.Join(root, ".claude.json")
	doc := `{"oauthAccount":{"accountUuid":"u","organizationUuid":"o"},"padding":"` + strings.Repeat("x", 4<<20) + `"}`
	if err := os.WriteFile(file, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	if err := os.Chtimes(file, at, at); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err != nil {
		t.Fatal(err)
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	ps, err := Load(dir)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := Find(ps, "claude", "default"); p.Account != "claude/u/o" {
		t.Fatalf("account %q", p.Account)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > 1<<20 {
		t.Fatalf("second load allocated %d bytes for a 4 MiB file it had already read", grew)
	}
}
