package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
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

func TestAGoalStartReadsOtherHostsOnlyWhenItDependsOnThem(t *testing.T) {
	here := t.TempDir()
	for _, tc := range []struct {
		name    string
		args    []string
		ssh     bool
		parent  string
		started bool
		// refusal is in the error when ym goal refuses; queued says the goal went to the host, whichever way ym reached it.
		refusal        string
		queued         bool
		down, noChiefs bool
	}{
		{name: "a local repository", args: []string{"--repo", here}, started: true},
		{name: "under a chief by host and name", args: []string{"--repo", here, "--under", "box/core-lead"}, ssh: true, parent: "box/mgr9", started: true},
		{name: "under a chief by host and id", args: []string{"--repo", here, "--under", "box/mgr9"}, ssh: true, parent: "box/mgr9", started: true},
		{name: "under a chief in upper case", args: []string{"--repo", here, "--under", "BOX/CORE-LEAD"}, ssh: true, parent: "box/mgr9", started: true},
		{name: "under a chief by @host", args: []string{"--repo", here, "--under", "@box/core-lead"}, ssh: true, parent: "box/mgr9", started: true},
		{name: "a repository another host holds", args: []string{"--repo", "/src/app"}, ssh: true},
		{name: "under a chief the host does not have", args: []string{"--repo", here, "--under", "box/nosuch"}, ssh: true, refusal: "core-lead"},
		{name: "under a chief by @host the host does not have", args: []string{"--repo", here, "--under", "@box/nosuch"}, ssh: true, refusal: "core-lead"},
		{name: "on the host, under a chief it does not have", args: []string{"--host", "box", "--repo", "app", "--under", "box/nosuch"}, ssh: true, refusal: "core-lead"},
		{name: "on the host, under its chief", args: []string{"--host", "box", "--repo", "app", "--under", "box/core-lead"}, ssh: true, queued: true},
		{name: "under a chief of a host that does not answer", args: []string{"--repo", here, "--under", "box/nosuch"}, ssh: true, down: true, parent: "box/nosuch", started: true},
		{name: "under a chief of a host known only through relays", args: []string{"--repo", here, "--under", "elsewhere/nosuch"}, ssh: true, parent: "elsewhere/nosuch", started: true},
		{name: "under a chief of a host with none to compare", args: []string{"--repo", here, "--under", "box/nosuch"}, ssh: true, noChiefs: true, parent: "box/nosuch", started: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("YAMNAYA_NO_SERVE", "1")
			dir := t.TempDir()
			roster := []core.Manager{{ID: "mgr9", Role: core.RoleChief, Name: "core-lead", Repo: "/src/app"}}
			if tc.noChiefs {
				roster = nil
			}
			managers, err := json.Marshal(roster)
			if err != nil {
				t.Fatal(err)
			}
			board := []byte(`{"managers":` + string(managers) + `,` + fakeBoard[1:])
			log := filepath.Join(dir, "ssh.log")
			if err := os.WriteFile(filepath.Join(dir, "board.json"), board, 0o644); err != nil {
				t.Fatal(err)
			}
			stub := filepath.Join(dir, "ssh")
			answer := "cat " + filepath.Join(dir, "board.json")
			if tc.down {
				answer = "exit 255"
			}
			if err := os.WriteFile(stub, []byte("#!/bin/sh\necho \"$*\" >> "+log+"\n"+answer+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YAMNAYA_SSH_BIN", stub)
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			if err := st.RecordHost("box", time.Now()); err != nil {
				t.Fatal(err)
			}
			k := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
			}
			cmd := goalCmd(with, with)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(append(tc.args, "ship the thing"))
			err = cmd.Execute()
			if tc.refusal != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refusal) {
					t.Errorf("ym goal %v = %v, want it refused naming %q", tc.args, err, tc.refusal)
				}
				if pending, _ := st.PendingRelays(); len(pending) != 0 {
					t.Errorf("queued %+v", pending)
				}
			} else if err != nil {
				t.Fatalf("ym goal %v: %v\n%s", tc.args, err, out.String())
			}
			if tc.queued {
				if !strings.Contains(out.String(), "goal for app on box") {
					t.Errorf("ym goal %v printed %q, want the goal sent to box", tc.args, out.String())
				}
				return
			}
			if _, err := os.Stat(log); (err == nil) != tc.ssh {
				t.Fatalf("ssh ran %v, want %v", err == nil, tc.ssh)
			}
			o, err := k.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if len(o.Managers) == 1 != tc.started {
				t.Fatalf("managers here %+v, want started here %v", o.Managers, tc.started)
			}
			for _, m := range o.Managers {
				if m.ParentID != tc.parent {
					t.Fatalf("parent %q, want %q", m.ParentID, tc.parent)
				}
			}
		})
	}
}

func TestTheNamerRunsOnALoginWithRoom(t *testing.T) {
	future := time.Now().Add(time.Hour)
	for _, tc := range []struct {
		name   string
		full   []string
		wantOn string
		called bool
	}{
		{name: "the default has room", wantOn: "", called: true},
		{name: "the default is out of its limit", full: []string{"default"}, wantOn: "work", called: true},
		{name: "every login is out", full: []string{"default", "work"}, called: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("HOME", dir)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			os.Unsetenv("CLAUDE_CONFIG_DIR")
			work := filepath.Join(dir, "work-root")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(work, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"u","organizationUuid":"o"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := profile.Add(dir, profile.Profile{Harness: claude.Name, ID: "work", Label: "work", Root: work}); err != nil {
				t.Fatal(err)
			}
			ps, err := profile.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			var limits []core.Limit
			for _, p := range ps {
				if p.Harness == claude.Name && slices.Contains(tc.full, p.ID) {
					limits = append(limits, core.Limit{Harness: claude.Name, Profile: p.ID, Account: p.Account, Used: 100, ResetsAt: future})
				}
			}
			bin := t.TempDir()
			log := filepath.Join(bin, "ran")
			stub := "#!/bin/sh\nprintf '%s' \"$CLAUDE_CONFIG_DIR\" > " + log + "\necho good-name\n"
			if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			l := launcher{records: launch.Records{Dir: filepath.Join(dir, "launches")}, core: &core.Core{Meter: fixedLimits(limits)}}
			got := l.haikuName("anything")
			ran, err := os.ReadFile(log)
			if tc.called != (err == nil) {
				t.Fatalf("claude ran %v, want %v; reply %q", err == nil, tc.called, got)
			}
			if !tc.called {
				return
			}
			want := ""
			if tc.wantOn == "work" {
				want = work
			}
			if string(ran) != want || got != "good-name" {
				t.Fatalf("ran on %q replying %q, want on %q", ran, got, want)
			}
		})
	}
}

type fixedLimits []core.Limit

func (f fixedLimits) Fleet(time.Time) core.Fleet { return core.Fleet{Limits: f} }

func TestWhatStartingAGoalOrChiefPrintsLeadsWithItsName(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		chief   bool
		service error
		want    []string
	}{
		{name: "a goal", args: []string{"--name", "ship-it", "--repo", "DIR", "ship the thing"}, want: []string{"ship-it: its manager", "brief: " + core.BriefURL("ship-it") + " ("}},
		{name: "a goal whose serve would not start", args: []string{"--name", "ship-it", "--repo", "DIR", "ship the thing"}, service: errors.New("no service"), want: []string{"ship-it recorded, but ym serve did not start"}},
		{name: "a chief", args: []string{"ops-lead"}, chief: true, want: []string{"chief ops-lead runs as", "brief: " + core.BriefURL("ops-lead") + " ("}},
		{name: "a chief whose serve would not start", args: []string{"ops-lead"}, chief: true, service: errors.New("no service"), want: []string{"chief ops-lead recorded, but ym serve did not start"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			k := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }, Launches: nameLaunches{}}
			was := startService
			startService = func() error { return tc.service }
			defer func() { startService = was }()
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
			}
			cmd := goalCmd(with, with)
			if tc.chief {
				cmd = chiefCmd(with, with)
			}
			args := slices.Clone(tc.args)
			for i := range args {
				if args[i] == "DIR" {
					args[i] = dir
				}
			}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("printed %q, want %q", out.String(), want)
				}
			}
			for _, line := range strings.Split(out.String(), "\n") {
				before, _, _ := strings.Cut(line, " (/")
				if strings.Contains(before, "goal1") || strings.Contains(before, "mgr1") {
					t.Errorf("line %q leads with an id", line)
				}
			}
		})
	}
}

// nameLaunches starts a manager as a session id, so a test sees what a real start prints.
type nameLaunches struct{ core.Launches }

func (nameLaunches) Start(core.NewLaunch) (core.Started, error) {
	return core.Started{Session: "abcd1234-0000"}, nil
}

func (nameLaunches) Dormant() []core.Dormant   { return nil }
func (nameLaunches) Launched() []core.Launched { return nil }

func TestGoalContextWithoutARefReadsTheCallersOwn(t *testing.T) {
	dir := t.TempDir()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	_, m, err := k.NewGoalNamed("ship-it", "ship the thing", dir, "", "")
	if err != nil {
		t.Fatal(err)
	}
	m.Session = "manager-session"
	if err := st.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			return run(&env{core: k, session: "manager-session"}, cmd, args)
		}
	}
	cmd := goalCmd(with, with)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"context"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ym goal context: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "ship-it revision 0") || !strings.Contains(out.String(), "ship the thing") {
		t.Fatalf("printed %q", out.String())
	}
}
