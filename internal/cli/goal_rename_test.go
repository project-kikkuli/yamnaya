package cli

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestGoalRenameRenamesAGoalOrChiefAndItsBriefPageAnswersToEveryRef(t *testing.T) {
	for _, tc := range []struct {
		name, ref, to, err string
	}{
		{name: "a chief by its name", ref: "data", to: "data-platform"},
		{name: "a chief by its id", ref: "mgr1", to: "data-platform"},
		{name: "its own name again", ref: "data", to: "data"},
		{name: "an invalid name", ref: "data", to: "Data Platform", err: "kebab-case"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", dir)
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			chief, err := c.NewChief("data", "", dir, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			root := NewRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"goal", "rename", tc.ref, tc.to})
			err = root.Execute()
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("ym goal rename %s %s = %v, want an error saying %q", tc.ref, tc.to, err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ym goal rename: %v\n%s", err, out.String())
			}
			srv := httptest.NewServer(&briefSite{core: c})
			defer srv.Close()
			for _, ref := range []string{tc.to, chief.ID} {
				res, err := http.Get(srv.URL + "/b/" + ref)
				if err != nil {
					t.Fatal(err)
				}
				body, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if page := html.UnescapeString(string(body)); res.StatusCode != http.StatusOK || !strings.Contains(page, "Chief "+tc.to) {
					t.Fatalf("/b/%s answered %d without naming chief %s:\n%s", ref, res.StatusCode, tc.to, page)
				}
			}
		})
	}
}

func TestGoalRenameOfAnotherHostsGoalRunsOnThatHost(t *testing.T) {
	for _, tc := range []struct {
		name, reply, want, err string
		fail                   bool
	}{
		{name: "the host renames it", reply: "login-box is now login-b2", want: "goal rename login-box login-b2"},
		{name: "the host refuses it", reply: "error: login-b2 already names a goal or chief here", fail: true, want: "goal rename login-box login-b2", err: "already names"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", dir)
			log := filepath.Join(dir, "ssh.log")
			status := 0
			if tc.fail {
				status = 1
			}
			stub := filepath.Join(dir, "ssh")
			script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %s\ncase \"$*\" in *rename*) echo '%s' >&2; exit %d ;; esac\necho '{}'\n", log, tc.reply, status)
			if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YAMNAYA_SSH_BIN", stub)
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			if err := st.RecordHost("box", time.Now()); err != nil {
				t.Fatal(err)
			}
			st.Close()
			root := NewRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs([]string{"goal", "rename", "box/login-box", "login-b2"})
			err = root.Execute()
			if tc.err == "" && err != nil || tc.err != "" && (err == nil || !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("ym goal rename = %v, want error %q\n%s", err, tc.err, out.String())
			}
			ran, err := os.ReadFile(log)
			if err != nil || !strings.Contains(string(ran), tc.want) {
				t.Fatalf("ssh ran %q, %v; want %q", ran, err, tc.want)
			}
		})
	}
}

func TestEveryCommandTakingAGoalOrChiefTakesItsNameOrId(t *testing.T) {
	for _, form := range []string{"name", "id"} {
		for _, tc := range []struct {
			name string
			args func(goal, chief string) []string
			want string
		}{
			{"goal list --under", func(_, chief string) []string { return []string{"goal", "list", "--under", chief} }, "ship-the-thing"},
			{"goal show a goal", func(goal, _ string) []string { return []string{"goal", "show", goal} }, "ship-the-thing"},
			{"goal show a chief", func(_, chief string) []string { return []string{"goal", "show", chief} }, "ops-lead"},
			{"goal context", func(goal, _ string) []string { return []string{"goal", "context", goal} }, "ship the thing"},
			{"goal feed", func(goal, _ string) []string { return []string{"goal", "feed", goal} }, "ship-the-thing"},
		} {
			t.Run(tc.name+" by "+form, func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("YAMNAYA_STATE_DIR", dir)
				st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
				g, m, err := c.NewGoalNamed("ship-the-thing", "ship the thing", dir, "", "")
				if err != nil {
					t.Fatal(err)
				}
				boss, err := c.NewChief("ops-lead", "", dir, "", []string{m.ID})
				if err != nil {
					t.Fatal(err)
				}
				goal, chief := g.Name, boss.Name
				if form == "id" {
					goal, chief = g.ID, boss.ID
				}
				root := NewRoot()
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&out)
				root.SetArgs(tc.args(goal, chief))
				if err := root.Execute(); err != nil {
					t.Fatalf("%v: %v\n%s", tc.args(goal, chief), err, out.String())
				}
				if !strings.Contains(out.String(), tc.want) {
					t.Fatalf("%v printed %q, want %q", tc.args(goal, chief), out.String(), tc.want)
				}
			})
		}
	}
}

func TestGoalUnderAChiefOnAnotherHostStoresTheChiefByItsIdGivenItsNameOrId(t *testing.T) {
	for _, ref := range []string{"box/core-lead", "box/mgr9", "BOX/CORE-LEAD"} {
		t.Run(ref, func(t *testing.T) {
			t.Setenv("YAMNAYA_NO_SERVE", "1")
			dir := t.TempDir()
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			k := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
			}
			hosts := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error {
					k.Remotes = &itemRemote{board: core.RemoteBoard{Host: "box", Managers: []core.Manager{{ID: "mgr9", Role: core.RoleChief, Name: "core-lead"}}}}
					return run(&env{core: k}, cmd, args)
				}
			}
			cmd := goalCmd(with, hosts)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--repo", dir, "--under", ref, "ship the thing"})
			if err := cmd.Execute(); err != nil {
				t.Fatalf("ym goal --under %s: %v\n%s", ref, err, out.String())
			}
			o, err := k.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if len(o.Managers) != 1 {
				t.Fatalf("managers %+v, want the goal's one", o.Managers)
			}
			for _, m := range o.Managers {
				if m.ParentID != "box/mgr9" {
					t.Fatalf("parent %q, want box/mgr9", m.ParentID)
				}
			}
		})
	}
}
