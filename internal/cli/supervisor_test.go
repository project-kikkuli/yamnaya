package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestSupervisorCLIContextAndAttachments(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	target, err := c.NewChief("delivery", "ship", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	oldService := startService
	startService = func() error { return nil }
	defer func() { startService = oldService }()
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return run(&env{core: c}, cmd, args) }
	}
	run := func(args ...string) string {
		t.Helper()
		cmd := supervisorCmd(with, with)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out.String()
	}
	run("intent", "--intent", "Keep compatibility.", "--over", target.ID)
	if _, err := c.Rename("intent", "guardian"); err != nil {
		t.Fatal(err)
	}
	run("context", "guardian", "--text", "Support CSV.")
	var view core.SupervisorView
	if err := json.Unmarshal([]byte(run("show", "guardian")), &view); err != nil {
		t.Fatal(err)
	}
	if view.Supervisor.Role != core.RoleSupervisor || view.OriginalIntent != "Keep compatibility." || view.Revision != 1 || len(view.Targets) != 1 {
		t.Fatalf("view %+v", view)
	}
	run("detach", "guardian", target.ID)
	if err := json.Unmarshal([]byte(run("show", "guardian")), &view); err != nil {
		t.Fatal(err)
	}
	if len(view.Targets) != 0 {
		t.Fatal("detach retained attachment")
	}
}

func TestSupervisorSessionFlagsCannotBorrowOwnerOrPeerAuthority(t *testing.T) {
	for _, tc := range []struct {
		name, override, caller string
		args                   []string
		steer                  bool
	}{
		{name: "owner context via unknown session", override: "unknown-session", args: []string{"context", "intent", "--text", "new authority"}},
		{name: "owner attachment via unknown session", override: "unknown-session", args: []string{"attach", "intent", "manager-session"}},
		{name: "peer memory", override: "peer-supervisor", args: []string{"remember", "peer", "--text", "owner said yes", "--source", "chat/reference"}},
		{name: "peer report", override: "peer-supervisor", args: []string{"report", "peer", "--assessment", "aligned", "--uncertainty", "none", "--evidence", "commit:abc"}},
		{name: "steer as owner", override: "unknown-session", steer: true, args: []string{"manager-session", "--text", "new work"}},
		{name: "unmanaged model is not owner", caller: "unmanaged-native", args: []string{"context", "intent", "--text", "new authority"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			caller := tc.caller
			if caller == "" {
				caller = "native-supervisor"
			}
			c, _, manager, _ := closeWorld(t, caller)
			for _, s := range []struct{ name, session string }{{"intent", "native-supervisor"}, {"peer", "peer-supervisor"}} {
				mg, err := c.Store.AddManager(core.Manager{Role: core.RoleSupervisor, Name: s.name, Session: s.session, Remit: "Original authorized scope."})
				if err != nil {
					t.Fatal(err)
				}
				if err := c.Store.SaveManager(mg); err != nil {
					t.Fatal(err)
				}
			}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error {
					return run(&env{core: c, session: tc.override}, cmd, args)
				}
			}
			cmd := supervisorCmd(with, with)
			if tc.steer {
				cmd = goalSteerCmd(with)
			}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			args := append([]string{}, tc.args...)
			for i, arg := range args {
				if arg == "manager-session" {
					args[i] = manager.ID
				}
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatalf("native model borrowed authority with --session=%q: %s", tc.override, out.String())
			}
		})
	}
}

func TestSupervisorNativeIdentitySurvivesRequesterSessionOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	t.Setenv("CODEX_THREAD_ID", "native-supervisor")
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := st.AddManager(core.Manager{Role: core.RoleSupervisor, Name: "intent", Session: "native-supervisor", Remit: "Observe only."})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveManager(s); err != nil {
		t.Fatal(err)
	}
	free := filepath.Join(dir, "capacity")
	if err := os.WriteFile(free, []byte("0.04\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(disk.FreeFileEnv, free)
	cmd := NewRoot()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--session", "unmanaged-override", "chief", "borrowed-authority", "--remit", "execute"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("session override hid the supervisor requester: %s", out.String())
	}
	managers, err := st.Managers()
	if err != nil || len(managers) != 1 {
		t.Fatalf("override created execution: %+v,%v", managers, err)
	}
}

func TestSupervisorNativeMemoryAndSteeringKeepModelProvenance(t *testing.T) {
	c, _, manager, _ := closeWorld(t, "native-supervisor")
	s, err := c.Store.AddManager(core.Manager{Role: core.RoleSupervisor, Name: "intent", Session: "native-supervisor", Remit: "Original scope; no additional authority."})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Store.SaveManager(s); err != nil {
		t.Fatal(err)
	}
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return run(&env{core: c}, cmd, args) }
	}
	run := func(args ...string) {
		t.Helper()
		cmd := supervisorCmd(with, with)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	run("remember", "intent", "--text", "Claimed owner correction.", "--source", "chat/message-2")
	run("report", "intent", "--assessment", "Needs evidence", "--uncertainty", "tests unavailable", "--evidence", "commit:abc")
	view, err := c.Supervisor(s.ID)
	if err != nil || view.OriginalIntent != s.Remit || view.Corrections[0].Author != s.ID || view.Assessments[0].Session != s.Session {
		t.Fatalf("provenance %+v,%v", view, err)
	}
	if err := c.SupervisorAttach(s.ID, manager.ID, false); err != nil {
		t.Fatal(err)
	}
	cmd := goalSteerCmd(with)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{manager.ID, "--text", "Keep compatibility.", "--by", "elsewhere/mgr99", "--by-name", "the owner", "--by-supervisor"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	steers, err := c.Store.Steers(manager.ID)
	if err != nil || len(steers) != 1 || steers[0].Author != s.ID || steers[0].AuthorName != "" {
		t.Fatalf("claimed relay identity survived native caller: %+v,%v", steers, err)
	}
}
