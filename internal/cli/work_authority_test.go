package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestWorkAuthoritySubprocess(t *testing.T) {
	path := os.Getenv("YM_WORK_AUTHORITY_TEST_STATE")
	if path == "" {
		t.Skip("private authority subprocess")
	}
	st, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k := &core.Core{Store: st}
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
	}
	cmd := workAuthorizeCmd(with)
	cmd.SetArgs([]string{os.Getenv("YM_WORK_AUTHORITY_TEST_ID")})
	if err = cmd.Execute(); err != nil {
		t.Fatal(err)
	}
}

func TestLiveAuthorityProtocolSerializesDispatchAndKill(t *testing.T) {
	for _, mode := range []string{"success", "kill before", "kill during", "dispatch failure", "authority disconnected"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, ".ym-rehearsal"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, "ym.db")
			st, err := core.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			g, m := launchAssignmentManager(t, st, "manager")
			k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			a, err := k.AssignWork(m.Session, "b/worker", "one", "work")
			if err != nil {
				t.Fatal(err)
			}
			w := core.RemoteAssignment{Origin: k.SelfName(), Host: "b", Goal: g.ID, Worker: "worker", Assignment: a, Launch: &core.NewLaunch{Session: "worker", Prompt: "work"}}
			raw, err := json.Marshal(w)
			if err != nil {
				t.Fatal(err)
			}
			db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(30000)")
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(`UPDATE ym_work_dispatches SET state='pending',body=? WHERE id=?`, string(raw), a.ID); err != nil {
				t.Fatal(err)
			}
			g, err = st.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "kill before" {
				g.State = core.GoalKilled
				if err = st.SaveGoal(g); err != nil {
					t.Fatal(err)
				}
			}
			binary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("YM_WORK_AUTHORITY_TEST_STATE", path)
			t.Setenv("YM_WORK_AUTHORITY_TEST_ID", a.ID)
			t.Setenv("YM_WORK_AUTHORITY_TEST_BINARY", binary)
			script := "#!/bin/sh\nunset YM_CLI_TEST_CHILD\nexec \"$YM_WORK_AUTHORITY_TEST_BINARY\" -test.run=^TestWorkAuthoritySubprocess$\n"
			if mode == "authority disconnected" {
				script = "#!/bin/sh\nexit 255\n"
			}
			ssh := filepath.Join(root, "ssh")
			if err = os.WriteFile(ssh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			r := newRemoteHosts([]string{"origin"}, 0, nil)
			r.ssh, r.timeout = ssh, 5*time.Second
			started := 0
			killed := make(chan error, 1)
			dispatchErr := errors.New("dispatch lost connection")
			err = r.AuthorizeWork("origin", a.ID, func(work core.RemoteAssignment) error {
				if work.Assignment.ID != a.ID {
					t.Fatalf("authority changed assignment: %+v", work)
				}
				if mode == "kill during" {
					g.State = core.GoalKilled
					go func() { killed <- st.SaveGoal(g) }()
				}
				if mode == "dispatch failure" {
					return dispatchErr
				}
				started++
				return nil
			})
			if mode == "kill during" && started == 1 {
				if err := <-killed; err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "kill before":
				if !errors.Is(err, core.ErrWorkRevoked) || started != 0 {
					t.Fatalf("revoked permit dispatched: %d %v", started, err)
				}
			case "authority disconnected":
				if !errors.Is(err, core.ErrUnreachable) || started != 0 {
					t.Fatalf("offline authority dispatched: %d %v", started, err)
				}
			case "dispatch failure":
				if !errors.Is(err, dispatchErr) {
					t.Fatal(err)
				}
			default:
				if err != nil || started != 1 {
					t.Fatalf("live authority failed: %d %v", started, err)
				}
			}
			var state string
			if err = db.QueryRow(`SELECT state FROM ym_work_dispatches WHERE id=?`, a.ID).Scan(&state); err != nil {
				t.Fatal(err)
			}
			expected := "started"
			if mode == "kill before" || mode == "kill during" {
				expected = "revoked"
			}
			if mode == "dispatch failure" || mode == "authority disconnected" {
				expected = "pending"
			}
			if state != expected {
				t.Fatalf("dispatch state %s, want %s", state, expected)
			}
		})
	}
}
