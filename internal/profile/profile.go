package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
)

type Profile struct {
	AuthOverride     bool   `json:"auth_override,omitempty"`
	Host             string `json:"host,omitempty"`
	Harness          string `json:"harness"`
	ID               string `json:"id"`
	Label            string `json:"label"`
	Account          string `json:"account,omitempty"`
	RootValue        string `json:"root_value,omitempty"`
	RootSet          bool   `json:"root_set,omitempty"`
	Root             string `json:"root"`
	SQLiteEnv        string `json:"sqlite_env,omitempty"`
	SQLiteEnvSet     bool   `json:"sqlite_env_set,omitempty"`
	SQLiteRoot       string `json:"sqlite_root,omitempty"`
	Explicit         bool   `json:"explicit,omitempty"`
	SecureStorage    string `json:"secure_storage,omitempty"`
	SecureStorageSet bool   `json:"secure_storage_set,omitempty"`
}

func ID(id string) string {
	if id == "" {
		return "default"
	}
	return id
}
func SessionKey(harness, id, session string) string {
	if ID(id) == "default" {
		return session
	}
	return harness + ":" + id + "@" + session
}
func EnvKey(harness string) string {
	if harness == "claude" {
		return "CLAUDE_CONFIG_DIR"
	}
	return "CODEX_HOME"
}
func Defaults() []Profile {
	home, _ := os.UserHomeDir()
	var out []Profile
	for _, h := range []string{"claude", "codex"} {
		rootValue, rootSet := os.LookupEnv(EnvKey(h))
		root := rootValue
		explicit := root != ""
		if root == "" {
			root = filepath.Join(home, "."+h)
		}
		secure, set := os.LookupEnv("CLAUDE_SECURESTORAGE_CONFIG_DIR")
		sqliteRoot := ""
		sqliteEnv, sqliteSet := os.LookupEnv("CODEX_SQLITE_HOME")
		if h == "codex" {
			sqliteRoot = canonical(SQLiteHome(root))
			secure = ""
			set = false
		} else {
			sqliteEnv = ""
			sqliteSet = false
		}
		out = append(out, Profile{AuthOverride: authEnvironment(h), Host: LocalHost(), Harness: h, ID: "default", Label: "default", Root: canonical(root), RootValue: rootValue, RootSet: rootSet, Explicit: explicit, SecureStorage: secure, SecureStorageSet: set, SQLiteRoot: sqliteRoot, SQLiteEnv: sqliteEnv, SQLiteEnvSet: sqliteSet})
	}
	return out
}
func path(dir string) string { return filepath.Join(dir, "profiles.json") }
func Load(dir string) ([]Profile, error) {
	out, err := defaultsAt(dir)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var saved []Profile
	if err = json.Unmarshal(b, &saved); err != nil {
		return nil, fmt.Errorf("profiles: %w", err)
	}
	for _, p := range saved {
		if err := validate(p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Harness != out[j].Harness {
			return out[i].Harness < out[j].Harness
		}
		if out[i].ID == "default" {
			return true
		}
		if out[j].ID == "default" {
			return false
		}
		return out[i].ID < out[j].ID
	})
	for i := range out {
		out[i].Host = LocalHost()
		out[i].Account = Identity(out[i])
		if out[i].Harness == "codex" && out[i].ID != "default" {
			out[i].SQLiteRoot = out[i].Root
		}
	}
	return out, nil
}
func Find(ps []Profile, harness, id string) (Profile, error) {
	for _, p := range ps {
		if p.Harness == harness && p.ID == ID(id) {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("no %s profile %q on this host", harness, ID(id))
}

var validID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func validate(p Profile) error {
	if p.Harness != "claude" && p.Harness != "codex" {
		return fmt.Errorf("unknown harness %q", p.Harness)
	}
	if !validID.MatchString(p.ID) || p.ID == "default" {
		return fmt.Errorf("profile id must be letters, digits, underscores or dashes; default is implicit")
	}
	if !filepath.IsAbs(p.Root) {
		return fmt.Errorf("profile root must be absolute")
	}
	return nil
}
func canonical(root string) string {
	if absolute, err := filepath.Abs(root); err == nil {
		root = absolute
	}
	if real, err := filepath.EvalSymlinks(root); err == nil {
		return real
	}
	parent := filepath.Dir(root)
	if parent != root {
		return filepath.Join(canonical(parent), filepath.Base(root))
	}
	return filepath.Clean(root)
}
func Add(dir string, p Profile) error {
	if err := validate(p); err != nil {
		return err
	}
	p.Root = canonical(p.Root)
	p.Explicit = true
	if p.Label == "" {
		p.Label = p.ID
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path(dir)+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	ps, err := Load(dir)
	if err != nil {
		return err
	}
	for _, q := range ps {
		if q.Harness == p.Harness && q.ID == p.ID {
			return fmt.Errorf("profile %s/%s already exists", p.Harness, p.ID)
		}
		qr, pr := canonical(q.Root), canonical(p.Root)
		if qr == pr || strings.HasPrefix(pr, qr+string(os.PathSeparator)) || strings.HasPrefix(qr, pr+string(os.PathSeparator)) {
			return fmt.Errorf("profile roots must be disjoint: %s", q.ID)
		}
	}
	saved := []Profile{}
	for _, q := range ps {
		if q.ID != "default" {
			saved = append(saved, q)
		}
	}
	saved = append(saved, p)
	b, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	return Write(path(dir), b)
}
func Write(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".ym-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	parent, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	return parent.Sync()
}
func (p Profile) Argv(args []string) []string {
	prefix := []string{"env"}
	for _, k := range []string{EnvKey(p.Harness), "YAMNAYA_PROFILE", "YAMNAYA_ACCOUNT"} {
		prefix = append(prefix, "-u", k)
	}
	if p.Harness == "claude" {
		prefix = append(prefix, "-u", "CLAUDE_SECURESTORAGE_CONFIG_DIR")
	}
	if p.Harness == "codex" {
		prefix = append(prefix, "-u", "CODEX_SQLITE_HOME")
	}
	if p.ID != "default" {
		for _, k := range subscriptionOverrides {
			prefix = append(prefix, "-u", k)
		}
	}
	if p.Explicit || p.RootSet || p.ID != "default" {
		prefix = append(prefix, EnvKey(p.Harness)+"="+p.NativeRoot())
	}
	if p.Harness == "claude" {
		if p.ID != "default" {
			prefix = append(prefix, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+p.Root)
		} else if p.SecureStorageSet {
			prefix = append(prefix, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+p.SecureStorage)
		}
	}
	if p.Harness == "codex" && p.ID != "default" {
		prefix = append(prefix, "CODEX_SQLITE_HOME="+p.Root)
	} else if p.Harness == "codex" && p.SQLiteEnvSet {
		prefix = append(prefix, "CODEX_SQLITE_HOME="+p.SQLiteEnv)
	}
	prefix = append(prefix, "YAMNAYA_PROFILE="+p.ID, "YAMNAYA_ACCOUNT="+p.Account)
	return append(prefix, args...)
}
func (p Profile) Environment(base []string) []string {
	out := []string{}
	for _, e := range base {
		k, _, _ := strings.Cut(e, "=")
		if k == EnvKey(p.Harness) || k == "YAMNAYA_PROFILE" || k == "YAMNAYA_ACCOUNT" || (p.Harness == "codex" && k == "CODEX_SQLITE_HOME") || (p.Harness == "claude" && k == "CLAUDE_SECURESTORAGE_CONFIG_DIR") {
			continue
		}
		if p.ID != "default" {
			drop := false
			for _, key := range subscriptionOverrides {
				if k == key {
					drop = true
				}
			}
			if drop {
				continue
			}
		}
		out = append(out, e)
	}
	if p.Explicit || p.RootSet || p.ID != "default" {
		out = append(out, EnvKey(p.Harness)+"="+p.NativeRoot())
	}
	if p.Harness == "claude" {
		if p.ID != "default" {
			out = append(out, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+p.Root)
		} else if p.SecureStorageSet {
			out = append(out, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+p.SecureStorage)
		}
	}
	if p.Harness == "codex" && p.ID != "default" {
		out = append(out, "CODEX_SQLITE_HOME="+p.Root)
	} else if p.Harness == "codex" && p.SQLiteEnvSet {
		out = append(out, "CODEX_SQLITE_HOME="+p.SQLiteEnv)
	}
	return append(out, "YAMNAYA_PROFILE="+p.ID, "YAMNAYA_ACCOUNT="+p.Account)
}
func defaultsAt(dir string) ([]Profile, error) {
	defaults := Defaults()
	if dir == "" {
		return defaults, nil
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return defaults, nil
	} else if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "profile-defaults.json")
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err == nil {
		var ps []Profile
		if err = json.Unmarshal(b, &ps); err != nil {
			return nil, err
		}
		for i := range ps {
			ps[i].AuthOverride = ps[i].AuthOverride || authEnvironment(ps[i].Harness)
			ps[i].Account = Identity(ps[i])
		}
		return ps, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if id := os.Getenv("YAMNAYA_PROFILE"); id != "" && id != "default" {
		home, _ := os.UserHomeDir()
		for i := range defaults {
			defaults[i].Root = filepath.Join(home, "."+defaults[i].Harness)
			defaults[i].Explicit = false
			defaults[i].RootValue = ""
			defaults[i].RootSet = false
			defaults[i].SecureStorage = ""
			defaults[i].SecureStorageSet = false
			defaults[i].SQLiteEnv = ""
			defaults[i].SQLiteEnvSet = false
			if defaults[i].Harness == "codex" {
				defaults[i].SQLiteRoot = sqliteHome(defaults[i].Root, false)
			}
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	b, err = json.Marshal(defaults)
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for i := range defaults {
		defaults[i].Account = Identity(defaults[i])
	}
	return defaults, nil
}

func SQLiteHome(root string) string { return sqliteHome(root, true) }

func sqliteHome(root string, inherit bool) string {
	if config, err := readCodexConfig(root); err == nil && config.SQLiteHome != "" {
		return config.SQLiteHome
	}
	if dir := os.Getenv("CODEX_SQLITE_HOME"); inherit && dir != "" {
		return dir
	}
	return root
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_/@%+=:,.-]+$`)

func Shell(args []string) string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if !shellSafe.MatchString(arg) {
			arg = "'" + strings.ReplaceAll(arg, "'", "'\"'\"'") + "'"
		}
		out = append(out, arg)
	}
	return strings.Join(out, " ")
}

func LocalHost() string {
	host, _ := os.Hostname()
	first, _, _ := strings.Cut(host, ".")
	return strings.ToLower(first)
}

func (p Profile) NativeRoot() string {
	if p.ID == "default" && p.RootSet {
		return p.RootValue
	}
	return p.Root
}
