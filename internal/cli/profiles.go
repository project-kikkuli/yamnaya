package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/usage"
	"github.com/spf13/cobra"
)

func profilesCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "profiles", Short: "List isolated Claude and Codex logins on this host", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		ps, err := profile.Load(e.core.StateDir)
		if err != nil {
			return err
		}
		limits := profileLimits(e.core.StateDir)
		if asJSON {
			var out []profileOutput
			for _, p := range ps {
				out = append(out, profileOutput{Profile: p, Status: profileStatus(p, limits)})
			}
			return writeJSON(cmd.OutOrStdout(), out)
		}
		for _, p := range ps {
			account := p.Account
			if account == "" {
				account = "identity unknown"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s [%s] %s  %s\n", p.Harness, p.ID, p.Label, account, p.Root, profileStatus(p, limits))
		}
		return nil
	})}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print profile metadata as JSON")
	var root, label string
	add := &cobra.Command{Use: "add <claude|codex> <id>", Short: "Register an isolated native config root without copying credentials", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if root == "" {
			root = filepath.Join(e.core.StateDir, "profiles", args[0], args[1])
		}
		if err := profile.Add(e.core.StateDir, profile.Profile{Harness: args[0], ID: args[1], Label: label, Root: root}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "added %s/%s; login: ym profiles login %s %s\n", args[0], args[1], args[0], args[1])
		return nil
	})}
	add.Flags().StringVar(&root, "root", "", "native config directory (default: new isolated root in ym state)")
	add.Flags().StringVar(&label, "label", "", "display label, independent of account identity")
	var deviceAuth bool
	login := &cobra.Command{Use: "login <claude|codex> <id>", Short: "Run native login in the selected profile", Args: func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(2)(cmd, args); err != nil {
			return err
		}
		if deviceAuth && args[0] != "codex" {
			return fmt.Errorf("--device-auth is supported only for Codex")
		}
		return nil
	}, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		ps, err := profile.Load(e.core.StateDir)
		if err != nil {
			return err
		}
		p, err := profile.Find(ps, args[0], args[1])
		if err != nil {
			return err
		}
		if err = p.ValidateSubscription(""); err != nil {
			return err
		}
		b, err := bin(p.Harness)
		if err != nil {
			return err
		}
		if err = os.MkdirAll(p.Root, 0700); err != nil {
			return err
		}
		argv := []string{"auth", "login", "--claudeai"}
		if p.Harness == "codex" {
			argv = []string{"login"}
			if p.ID != "default" {
				argv = []string{"-c", "sqlite_home=" + fmt.Sprintf("%q", p.Root), "login"}
			}
			if deviceAuth {
				argv = append(argv, "--device-auth")
			}
		}
		meter := usage.New(e.core.StateDir)
		meter.Fetch = nil
		meter.Profiles = []profile.Profile{p}
		defer meter.Close()
		if _, err := meter.Scan(time.Now()); err != nil {
			return err
		}
		child := exec.Command(b, argv...)
		child.Env = p.Environment(os.Environ())
		child.Stdin = cmd.InOrStdin()
		child.Stdout = cmd.OutOrStdout()
		child.Stderr = cmd.ErrOrStderr()
		loginErr := child.Run()
		p.Account = profile.Identity(p)
		meter.Profiles = []profile.Profile{p}
		_, scanErr := meter.Scan(time.Now())
		return errors.Join(loginErr, scanErr)
	})}
	login.Flags().BoolVar(&deviceAuth, "device-auth", false, "use Codex device authorization on a host without a browser")
	cmd.AddCommand(add, login)
	return cmd
}
func recordProfile(rec launch.Record) profile.Profile {
	dir, _ := stateDir()
	ps, _ := profile.Load(dir)
	p, err := profile.Find(ps, rec.Harness, rec.Profile)
	if err != nil {
		p = profile.Profile{Harness: rec.Harness, ID: profile.ID(rec.Profile), Label: profile.ID(rec.Profile)}
	}
	if rec.ConfigRoot != "" {
		p.Root = rec.ConfigRoot
		p.SQLiteRoot = rec.SQLiteRoot
		p.SQLiteEnv = rec.SQLiteEnv
		p.SQLiteEnvSet = rec.SQLiteEnvSet
		p.RootValue = rec.RootValue
		p.RootSet = rec.RootSet
		p.Explicit = rec.ConfigExplicit
		p.SecureStorage = rec.SecureStorage
		p.SecureStorageSet = rec.SecureStorageSet
	}
	return p
}
func setRecordProfile(rec *launch.Record, p profile.Profile) {
	rec.Account = p.Account
	rec.Profile = p.ID
	rec.ConfigRoot = p.Root
	rec.SQLiteRoot = p.SQLiteRoot
	rec.SQLiteEnv = p.SQLiteEnv
	rec.SQLiteEnvSet = p.SQLiteEnvSet
	rec.RootValue = p.RootValue
	rec.RootSet = p.RootSet
	rec.ConfigExplicit = p.Explicit
	rec.SecureStorage = p.SecureStorage
	rec.SecureStorageSet = p.SecureStorageSet
}
func recordClaude(rec launch.Record) *claude.Adapter {
	a := claude.New()
	if rec.ConfigRoot != "" {
		a.Dir = rec.ConfigRoot
	}
	return a
}
func recordCodex(rec launch.Record) *codex.Adapter {
	a := codex.New()
	if rec.ConfigRoot != "" {
		a.Home = rec.ConfigRoot
		a.StateHome = rec.SQLiteRoot
		if rec.Profile != "" && rec.Profile != "default" {
			a.StateHome = rec.ConfigRoot
		}
	}
	return a
}
func scopedArgv(rec launch.Record, args []string) []string {
	if rec.ConfigRoot == "" {
		return args
	}
	if rec.Key != "" {
		args = append([]string{"env", "YAMNAYA_LAUNCH_KEY=" + rec.Key}, args...)
	}
	return recordProfile(rec).Argv(args)
}
func trustRecord(rec launch.Record) error {
	return launch.TrustRoot(rec.Harness, rec.ConfigRoot, rec.Worktree.Path, rec.ConfigExplicit)
}
func (l launcher) selectProfile(harness, id string) (profile.Profile, error) {
	dir := filepath.Dir(l.records.Dir)
	ps, err := profile.Load(dir)
	if err != nil {
		return profile.Profile{}, err
	}
	if id != "" && id != "auto" {
		p, err := profile.Find(ps, harness, id)
		if err != nil {
			return p, err
		}
		return p, p.DefaultConstraint()
	}
	var limits []core.Limit
	if l.core != nil && l.core.Meter != nil {
		limits = l.core.Meter.Fleet(time.Now()).Limits
	} else {
		limits = profileLimits(dir)
	}
	exhausted := map[string]bool{}
	blocked := map[string]bool{}
	for _, w := range limits {
		if w.Harness == harness && w.Used >= 100 && w.ResetsAt.After(time.Now()) {
			if w.Account == "" {
				blocked[profile.ID(w.Profile)] = true
			} else {
				exhausted[w.Account] = true
			}
		}
	}
	for _, p := range ps {
		if p.Harness != harness || p.DefaultConstraint() != nil || blocked[p.ID] || exhausted[p.Account] {
			continue
		}
		if p.ID == "default" || p.Account != "" {
			return p, nil
		}
	}
	if p, err := profile.Find(ps, harness, ""); err == nil {
		if issue := p.DefaultConstraint(); issue != nil {
			return profile.Profile{}, issue
		}
	}
	return profile.Profile{}, fmt.Errorf("no verified usable %s login; inspect ym limits and choose --profile explicitly", harness)
}

func profileLimits(dir string) []core.Limit { return usage.KnownLimits(dir, time.Now()) }

func profileDisplay(h, id string) string {
	if id == "" || id == "default" {
		return h
	}
	return h + "/" + id
}

func sqliteRoot(p profile.Profile) string {
	if p.SQLiteRoot != "" {
		return p.SQLiteRoot
	}
	return p.Root
}

func profileUsable(p profile.Profile, ls []core.Limit, now time.Time) bool {
	for _, l := range ls {
		if l.Harness == p.Harness && l.Used >= 100 && l.ResetsAt.After(now) && ((l.Account != "" && l.Account == p.Account) || (l.Account == "" && profile.ID(l.Profile) == p.ID)) {
			return false
		}
	}
	return true
}
func provisionProfile(p profile.Profile) error {
	if p.ID == "default" || p.Root == "" {
		return nil
	}
	if err := os.MkdirAll(p.Root, 0700); err != nil {
		return err
	}
	if p.Harness == "claude" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		home, _ := os.UserHomeDir()
		if _, err = wireProfileStatusline(p.Root, home, self); err != nil {
			return err
		}
	}
	target := filepath.Join(p.Root, "skills", "ym")
	if _, err := os.Lstat(target); os.IsNotExist(err) {
		if err = writeSkill(target); err != nil {
			return err
		}
	}
	ps, err := profile.Load(func() string { dir, _ := stateDir(); return dir }())
	if err != nil {
		return err
	}
	defaults, err := profile.Find(ps, p.Harness, "")
	if err != nil {
		return err
	}
	name := "AGENTS.md"
	if p.Harness == "claude" {
		name = "CLAUDE.md"
	}
	source := filepath.Join(defaults.Root, name)
	dest := filepath.Join(p.Root, name)
	if _, err := os.Stat(source); err == nil {
		if _, err = os.Lstat(dest); os.IsNotExist(err) {
			return os.Symlink(source, dest)
		}
	}
	return nil
}

type profileOutput struct {
	profile.Profile
	Status string `json:"status"`
}

func profileStatus(p profile.Profile, ls []core.Limit) string {
	if err := p.DefaultConstraint(); err != nil {
		return "unsupported: " + err.Error()
	}
	var used []string
	for _, l := range ls {
		if l.Harness != p.Harness || ((l.Account != "" && l.Account != p.Account) || (l.Account == "" && profile.ID(l.Profile) != p.ID)) {
			continue
		}
		if l.Used >= 100 {
			return "exhausted until " + l.ResetsAt.Format(time.RFC3339)
		}
		used = append(used, fmt.Sprintf("%s %.0f%%", l.Label(), l.Used))
	}
	if len(used) == 0 {
		return "quota unknown"
	}
	return strings.Join(used, ", ")
}
