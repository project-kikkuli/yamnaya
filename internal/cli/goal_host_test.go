package cli

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// relayNet carries relay round trips between in-process hosts by name.
type relayNet map[string]*core.Core

func (n relayNet) Sync(host string, req core.RelaySync) (core.RelayReply, error) {
	reply, err := n[host].ServeSync(req)
	if reply.Entries != nil {
		return reply, nil
	}
	return reply, err
}

func relayHost(t *testing.T, roots string) *core.Core {
	t.Helper()
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil },
		Roots: func() []string { return []string{roots} }}
}

// goalRun runs ym goal args on c through its own goal command, as session when it is set.
func goalRun(c *core.Core, session string, args ...string) (string, error) {
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, a []string) error { return run(&env{core: c, session: session}, cmd, a) }
	}
	cmd := goalCmd(with, with)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return strings.TrimSpace(out.String()), err
}

// runGoalCmd is ym serve's RunSelf for c: a relayed ym goal command run through this ym's own goal command.
func runGoalCmd(c *core.Core) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		out, err := goalRun(c, "", args[1:]...)
		if err != nil {
			return "", fmt.Errorf("ym %s: %w", strings.Join(args, " "), err)
		}
		return out, nil
	}
}

// relayedHosts are a, whose ym serve pulls, and b, reached only by a's calls, with a chief on b; a has repositories app, svc-one
// and svc-two under roots.
func relayedHosts(t *testing.T) (a, b *core.Core, chief core.Manager, sync func(), roots string) {
	t.Helper()
	t.Setenv("CODEX_THREAD_ID", "")
	roots, _ = filepath.EvalSymlinks(t.TempDir())
	for _, r := range []string{"app", "svc-one", "svc-two"} {
		if err := os.MkdirAll(filepath.Join(roots, r, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	a, b = relayHost(t, roots), relayHost(t, t.TempDir())
	a.RunSelf = runGoalCmd(a)
	sync = func() {
		t.Helper()
		if err := a.SyncRelays([]string{"b"}, relayNet{"b": b}); err != nil && !errors.Is(err, core.ErrFinal) && !strings.Contains(err.Error(), "relay ym") {
			t.Fatal(err)
		}
	}
	sync()
	chief, err := b.NewChief("platform", "", t.TempDir(), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	chief.Session = "chief-session"
	if err := b.Store.SaveManager(chief); err != nil {
		t.Fatal(err)
	}
	return a, b, chief, sync, roots
}

func managersOn(t *testing.T, c *core.Core) []core.Manager {
	t.Helper()
	o, err := c.LoadOrg()
	if err != nil {
		t.Fatal(err)
	}
	var started []core.Manager
	for _, m := range o.Managers {
		if m.Role == core.RoleManager {
			started = append(started, m)
		}
	}
	return started
}

func told(t *testing.T, c *core.Core, session string) string {
	t.Helper()
	msgs, err := c.Store.Messages(session)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, m := range msgs {
		all = append(all, m.Text)
	}
	return strings.Join(all, "\n")
}

func nick(t *testing.T, c *core.Core, host string) string {
	t.Helper()
	nicks, err := c.Store.TextNicknames()
	if err != nil || nicks[host] == "" {
		t.Fatalf("%s has no nickname: %v, %v", host, nicks, err)
	}
	return "@" + nicks[host]
}

func TestAGoalAskedForWithHostStartsOnTheHostThatPullsOrTellsTheRequesterWhyNot(t *testing.T) {
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	for _, tc := range []struct {
		name, repo, harness, profile string
		// host and under are how the requester writes a and its chief; under "" leaves the calling chief to default.
		host, under func(a, b *core.Core, chief string) string
		// refusal is in the requester's item when a cannot start the goal; "" means it starts. outside asks from no session, so the
		// owner gets the item.
		refusal string
		outside bool
	}{
		{name: "codex and a profile, by host name", repo: "app", harness: "codex", profile: "work"},
		{name: "by the host's nickname", repo: "app", host: func(a, b *core.Core, _ string) string { return nick(t, b, a.SelfName()) }},
		{name: "under the chief by id", repo: "app", under: func(_, _ *core.Core, chief string) string { return chief }},
		{name: "under the chief qualified by its host", repo: "app", under: func(_, _ *core.Core, chief string) string { return "b/" + chief }},
		{name: "under the chief qualified by its host's nickname", repo: "app",
			under: func(_, b *core.Core, chief string) string { return nick(t, b, "b") + "/" + chief }},
		{name: "a repository name that matches two", repo: "svc", refusal: `"svc" matches several repositories`},
		{name: "a repository the host does not have", repo: "nowhere", refusal: `no repository matches "nowhere"`},
		{name: "a repository path the host does not have", repo: "/definitely/not/here", refusal: `/definitely/not/here does not exist`},
		{name: "a repository the host does not have, asked from outside a session", repo: "nowhere", outside: true,
			refusal: `no repository matches "nowhere"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, chief, sync, roots := relayedHosts(t)
			host := a.SelfName()
			if tc.host != nil {
				host = tc.host(a, b, chief.ID)
			}
			args := []string{"--host", host, "--repo", tc.repo}
			if tc.harness == "codex" {
				args = append(args, "--codex")
			}
			if tc.profile != "" {
				args = append(args, "--profile", tc.profile)
			}
			if tc.under != nil {
				args = append(args, "--under", tc.under(a, b, chief.ID))
			}
			session, to := chief.Session, chief.ID
			if tc.outside {
				session, to = "", core.Owner
				args = append(args, "--under", chief.ID)
			}
			if out, err := goalRun(b, session, append(args, "ship the fix")...); err != nil {
				t.Fatalf("ym goal on b: %v: %s", err, out)
			}
			for range 3 {
				sync()
			}
			started := managersOn(t, a)
			items, err := b.Store.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.refusal != "" {
				if len(started) != 0 {
					t.Errorf("a started %+v", started)
				}
				if len(items) != 1 || items[0].Recipient != to || !strings.Contains(items[0].Question, tc.refusal) {
					t.Fatalf("b's items %+v; want one to %s carrying %q", items, to, tc.refusal)
				}
				return
			}
			if len(items) != 0 {
				t.Errorf("b has items %+v after the goal started", items)
			}
			harness := tc.harness
			if harness == "" {
				harness = "claude"
			}
			if len(started) != 1 {
				t.Fatalf("a started %+v; want one manager", started)
			}
			m := started[0]
			if m.Repo != filepath.Join(roots, "app") || m.Harness != harness || m.Profile != tc.profile || m.ParentID != "b/"+chief.ID {
				t.Errorf("a started %s in %s on %s with profile %q under %s; want app on %s with %q under b/%s",
					m.ID, m.Repo, m.Harness, m.Profile, m.ParentID, harness, tc.profile, chief.ID)
			}
			if heard := told(t, b, chief.Session); !strings.Contains(heard, m.Name) {
				t.Errorf("requester was told %q, want the started goal %s", heard, m.Name)
			}
		})
	}
}

func TestAGoalWithHostNamingAPathOnlyTheCallerCanReadIsRefusedWhereItIsAsked(t *testing.T) {
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	for _, repo := range []string{".", "..", "./app", "sub/app"} {
		t.Run(repo, func(t *testing.T) {
			a, b, chief, _, _ := relayedHosts(t)
			out, err := goalRun(b, chief.Session, "--host", a.SelfName(), "--repo", repo, "ship the fix")
			if err == nil {
				t.Fatalf("ym goal --repo %s = %q, want it refused", repo, out)
			}
			if pending, _ := b.Store.PendingRelays(); len(pending) != 0 {
				t.Errorf("b queued %+v", pending)
			}
		})
	}
}

func TestARelayedStartRunsOnceWhenItsWorkServiceCannotStart(t *testing.T) {
	for _, tc := range []struct {
		name string
		role string
		args []string
		cmd  func(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command
	}{
		{"a goal", core.RoleManager, []string{"goal", "--repo", "app", "--", "ship the fix"},
			func(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
				return goalCmd(with, with)
			}},
		{"a chief", core.RoleChief, []string{"chief", "--repo", "app", "second"},
			func(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
				return chiefCmd(with, with)
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YAMNAYA_NO_SERVE", "0")
			file := filepath.Join(t.TempDir(), "a-file")
			if err := os.WriteFile(file, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YAMNAYA_STATE_DIR", filepath.Join(file, "state"))
			if err := ensureWorkService(); err == nil {
				t.Fatal("the work service started; this test needs it to fail before it spawns one")
			}
			a, b, _, sync, _ := relayedHosts(t)
			a.RunSelf = func(args ...string) (string, error) {
				with := func(run runFunc) func(*cobra.Command, []string) error {
					return func(cmd *cobra.Command, x []string) error { return run(&env{core: a}, cmd, x) }
				}
				cmd := tc.cmd(with)
				var out bytes.Buffer
				cmd.SetOut(&out)
				cmd.SetErr(&out)
				cmd.SetArgs(args[1:])
				cmd.SilenceUsage, cmd.SilenceErrors = true, true
				if err := cmd.Execute(); err != nil {
					return "", fmt.Errorf("ym %s: %w", strings.Join(args, " "), err)
				}
				return out.String(), nil
			}
			if _, err := b.RunOrRelay(a.SelfName(), tc.args...); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				sync()
			}
			o, err := a.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			n := 0
			for _, m := range o.Managers {
				if m.Role == tc.role {
					n++
				}
			}
			if n != 1 {
				t.Errorf("a recorded %d, want one", n)
			}
		})
	}
}
