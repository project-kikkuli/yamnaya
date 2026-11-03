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

// runGoalCmd is ym serve's RunSelf for c: a relayed ym goal command run through this ym's own goal command.
func runGoalCmd(c *core.Core) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		with := func(run runFunc) func(*cobra.Command, []string) error {
			return func(cmd *cobra.Command, a []string) error { return run(&env{core: c}, cmd, a) }
		}
		cmd := goalCmd(with, with)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args[1:])
		cmd.SilenceUsage, cmd.SilenceErrors = true, true
		if err := cmd.Execute(); err != nil {
			return "", fmt.Errorf("ym %s: %w", strings.Join(args, " "), err)
		}
		return strings.TrimSpace(out.String()), nil
	}
}

func TestAGoalRelayedToTheHostThatPullsStartsThereAsAskedOrTellsTheRequesterWhyNot(t *testing.T) {
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	for _, tc := range []struct {
		name, repo, harness, profile string
		// under names b's chief as the requester writes it: bare, qualified by b's host, or by b's nickname.
		under func(c *core.Core, chief string) string
		// refusal is in the requester's item when a cannot start the goal; "" means it starts. outside asks from no session, so the
		// owner gets the item.
		refusal string
		outside bool
	}{
		{name: "codex and a profile, under the chief by id", repo: "app", harness: "codex", profile: "work",
			under: func(_ *core.Core, chief string) string { return chief }},
		{name: "under the chief qualified by its host", repo: "app",
			under: func(_ *core.Core, chief string) string { return "b/" + chief }},
		{name: "under the chief qualified by its host's nickname", repo: "app",
			under: func(c *core.Core, chief string) string {
				nicks, err := c.Store.TextNicknames()
				if err != nil || nicks["b"] == "" {
					t.Fatalf("b has no nickname: %v, %v", nicks, err)
				}
				return "@" + nicks["b"] + "/" + chief
			}},
		{name: "a repository name that matches two", repo: "svc", refusal: `"svc" matches several repositories`,
			under: func(_ *core.Core, chief string) string { return chief }},
		{name: "a repository the host does not have", repo: "nowhere", refusal: `no repository matches "nowhere"`,
			under: func(_ *core.Core, chief string) string { return chief }},
		{name: "a repository the host does not have, asked from outside a session", repo: "nowhere", outside: true,
			refusal: `no repository matches "nowhere"`, under: func(_ *core.Core, chief string) string { return chief }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roots, _ := filepath.EvalSymlinks(t.TempDir())
			for _, r := range []string{"app", "svc-one", "svc-two"} {
				if err := os.MkdirAll(filepath.Join(roots, r, ".git"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			a, b := relayHost(t, roots), relayHost(t, t.TempDir())
			a.RunSelf = runGoalCmd(a)
			net := relayNet{"b": b}
			sync := func() {
				t.Helper()
				if err := a.SyncRelays([]string{"b"}, net); err != nil && !errors.Is(err, core.ErrFinal) {
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
			var selected []string
			if tc.profile != "" {
				selected = []string{tc.profile}
			}
			requester, to := chief.Session, chief.ID
			if tc.outside {
				requester, to = "", core.Owner
			}
			if _, err := b.NewGoalOn(a.SelfName(), "ship the fix", tc.repo, tc.harness, tc.under(b, chief.ID), requester, selected...); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				sync()
			}
			o, err := a.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			var started []core.Manager
			for _, m := range o.Managers {
				if m.Role == core.RoleManager {
					started = append(started, m)
				}
			}
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
		})
	}
}
