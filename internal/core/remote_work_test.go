package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func remoteWorkFleet(t *testing.T) (*fleet, string) {
	t.Helper()
	f := newFleet(t)
	repo := filepath.Join(t.TempDir(), "b", "app")
	f.b.core.RunSelf = func(args ...string) (string, error) {
		if len(args) != 2 || args[0] != "work-receive" {
			return "", fmt.Errorf("unexpected command %v", args)
		}
		var m WorkMessage
		if err := json.Unmarshal([]byte(args[1]), &m); err != nil {
			return "", err
		}
		return "remote work recorded", f.b.core.WorkReceive(m)
	}
	r := &sshTo{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "b", Goals: []Goal{{ID: "remote", Repo: repo}}}}}, hosts: map[string]*orgWorld{"b": f.b}}
	f.a.core.Remotes = r
	return f, repo
}

func syncWork(t *testing.T, f *fleet) {
	t.Helper()
	for range 5 {
		f.sync(t)
	}
}

func TestRemoteOwnedLaunchAndReportUseOnlyTheOriginLedger(t *testing.T) {
	for _, mode := range []struct {
		queued  bool
		harness string
	}{{false, "claude"}, {true, "claude"}, {false, "codex"}, {true, "codex"}} {
		queued := mode.queued
		t.Run(fmt.Sprintf("queued=%v/%s", queued, mode.harness), func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "implement remote work", "/repo/app")
			if queued {
				f.a.core.Remotes = nil
				f.net.down["b"] = true
				// Repository discovery uses a cached remote board while transport is disconnected.
				f.a.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "b", Goals: []Goal{{Repo: repo}}}}}}
			}
			n := NewLaunch{Parent: m.Session, Repo: repo, Prompt: "implement", Session: "remote-worker", Source: "remote:stable", Harness: mode.harness}
			if _, err := f.a.core.StartNew(n); err != nil {
				t.Fatal(err)
			}
			if queued && len(f.b.launches.starts) != 0 {
				t.Fatal("disconnected launch ran")
			}
			f.net.down["b"] = false
			syncWork(t, f)
			assignments, err := f.a.core.Store.Assignments(g.ID)
			if err != nil || len(assignments) != 1 {
				t.Fatalf("origin obligations: %+v %v", assignments, err)
			}
			a := assignments[0]
			worker := "remote-worker"
			if mode.harness == "codex" {
				worker = "thread-1"
			}
			if a.Session != "b/"+worker || a.State != "outstanding" {
				t.Fatalf("actual destination identity missing: %+v", a)
			}
			if local, _ := f.b.core.Store.Assignments(g.ID); len(local) != 0 {
				t.Fatalf("destination became completion authority: %+v", local)
			}
			if _, err = f.a.core.StartNew(n); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if len(f.b.launches.starts) != 1 {
				t.Fatalf("replayed remote launch ran %d processes", len(f.b.launches.starts))
			}
			if _, _, err = f.a.core.FinishGoal(m.Session, "done", nil); err == nil {
				t.Fatal("outstanding remote work passed")
			}
			// Reports remain durable while their destination is unreachable.
			f.net.down["b"] = true
			if _, err = f.b.core.ReportAssignment(worker, a.ID, "implemented and committed"); err != nil {
				t.Fatal(err)
			}
			current, _ := f.a.core.Store.Assignments(g.ID)
			if current[0].Report != 0 {
				t.Fatal("disconnected report changed origin")
			}
			f.net.down["b"] = false
			syncWork(t, f)
			current, _ = f.a.core.Store.Assignments(g.ID)
			if current[0].Report == 0 {
				t.Fatalf("report did not arrive: %+v", current)
			}
			report := current[0].Report
			if err = f.a.core.AcceptAssignment(m.Session, a.ID, report); err != nil {
				t.Fatal(err)
			}
			if _, err = f.b.core.ReportAssignment(worker, a.ID, "replayed terminal result"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			after, _ := f.a.core.Store.Assignments(g.ID)
			if after[0].Report != report || after[0].State != "accepted" {
				t.Fatalf("replay changed acceptance: %+v", after)
			}
			if _, _, err = f.a.core.FinishGoal(m.Session, "done", nil); err != nil {
				t.Fatal(err)
			}
			f.a.supervise(t)
			v, err := f.a.core.Store.OpenVerification(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.a.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			final, _ := f.a.core.Store.Goal(g.ID)
			if final.State != GoalDone {
				t.Fatalf("accepted remote work did not complete: %+v", final)
			}
		})
	}
}

type remoteDisconnected struct{ fakeRemotes }

func (r *remoteDisconnected) Run(string, ...string) (string, error) { return "", ErrUnreachable }

func TestRemoteNestedLaunchAndGoWaitForOriginCommit(t *testing.T) {
	f, repo := remoteWorkFleet(t)
	g, m := f.a.goal(t, "remote nested work", "/repo/app")
	if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "root work", Session: "root-worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	root, _ := f.a.core.Store.Assignments(g.ID)
	// Native descendants retain origin ownership through persisted lineage.
	f.b.spawn("native", "root-worker")
	it, err := f.b.core.AddItem(Item{Session: "native", Kind: KindGo, Question: "implement follow-on"})
	if err != nil {
		t.Fatal(err)
	}
	if it.GoalOwner != "a/"+g.ID || it.Recipient != "a/"+m.ID {
		t.Fatalf("native go lost origin ownership: %+v", it)
	}
	report, err := f.a.core.ReportAssignment(m.Session, root[0].ID, "first work terminal")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.a.core.AcceptAssignment(m.Session, root[0].ID, report); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.a.core.FinishGoal(m.Session, "research complete", nil); err != nil {
		t.Fatal(err)
	}
	f.a.supervise(t)
	v, err := f.a.core.Store.OpenVerification(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.a.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := f.a.core.Store.Goal(g.ID)
	if _, _, err = f.b.core.Go(f.b.board(t), it.Key()); err != nil {
		t.Fatal(err)
	}
	pending, _ := f.b.core.Store.Item(it.Key())
	if pending.State != ItemOpen {
		t.Fatal("go delivered before origin withdrawal")
	}
	unchanged, _ := f.a.core.Store.Goal(g.ID)
	if unchanged.State != GoalDone {
		t.Fatal("untransported go changed origin")
	}
	syncWork(t, f)
	reopened, _ := f.a.core.Store.Goal(g.ID)
	delivered, _ := f.b.core.Store.Item(it.Key())
	if reopened.State != GoalActive || reopened.WorkRevision != before.WorkRevision+1 || delivered.State != ItemAnswered {
		t.Fatalf("go ordering: %+v %+v", reopened, delivered)
	}
	f.net.down["b"] = true
	if _, err = f.b.core.StartNew(NewLaunch{Parent: "native", Repo: "/repo/app", Prompt: "nested implementation", Session: "nested-worker", Source: "nested"}); err != nil {
		t.Fatal(err)
	}
	if len(f.b.launches.starts) != 1 {
		t.Fatal("nested process launched before origin registered it")
	}
	f.net.down["b"] = false
	syncWork(t, f)
	assignments, _ := f.a.core.Store.Assignments(g.ID)
	if len(assignments) != 2 || len(f.b.launches.starts) != 2 {
		t.Fatalf("nested assignment/launch: %+v %+v", assignments, f.b.launches.starts)
	}
	for _, a := range assignments {
		if strings.HasSuffix(a.Session, "nested-worker") && a.State != "outstanding" {
			t.Fatalf("nested obligation settled without acceptance: %+v", a)
		}
	}
}

func TestRemoteAssignmentApprovalReplayAndRestartPreserveGeneration(t *testing.T) {
	f, repo := remoteWorkFleet(t)
	g, m := f.a.goal(t, "remote native work", "/repo/app")
	if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "root", Session: "worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	a, err := f.b.core.AssignWork("worker", "planned", "native", "native child")
	if !errors.Is(err, ErrWorkPending) {
		t.Fatalf("native registration did not wait for authority: %+v %v", a, err)
	}
	if err = f.b.core.BindAssignment("worker", a.ID, "child"); !errors.Is(err, ErrWorkPending) {
		t.Fatalf("unapproved native launch bound: %v", err)
	}
	syncWork(t, f)
	approved, err := f.b.core.AssignWork("worker", "planned", "native", "native child")
	if err != nil {
		t.Fatal(err)
	}
	if approved.ID != a.ID || approved.Generation == 0 {
		t.Fatalf("approval changed assignment: %+v %+v", a, approved)
	}
	if err = f.b.core.BindAssignment("worker", a.ID, "child"); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	if _, err = f.b.core.ReportAssignment("child", a.ID, "native child terminal"); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	origin, _ := f.a.core.Store.Assignments(g.ID)
	if len(origin) != 2 || origin[1].Report == 0 || origin[1].Generation != approved.Generation {
		t.Fatalf("native identity/report routing: %+v", origin)
	}
	path := f.b.core.Store.memo.files[0]
	if err = f.b.core.Store.Close(); err != nil {
		t.Fatal(err)
	}
	f.b.core.Store, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.b.core.Store.Close() })
	if _, err = f.b.core.ReportAssignment("child", a.ID, "duplicate report after restart"); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	after, _ := f.a.core.Store.Assignments(g.ID)
	if after[1].Report != origin[1].Report || after[1].Generation != origin[1].Generation {
		t.Fatalf("restart replay changed terminal claim: %+v %+v", origin, after)
	}
}

func TestRemoteNestedThirdHostLaunchRetainsOriginAuthority(t *testing.T) {
	for _, queued := range []bool{false, true} {
		t.Run(fmt.Sprintf("queued=%v", queued), func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "three-host work", "/repo/app")
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "root", Session: "root", Source: "root"}); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			thirdRepo := filepath.Join(t.TempDir(), "c", "app")
			f.b.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "c", Goals: []Goal{{Repo: thirdRepo}}}}}}
			f.c.core.RunSelf = func(args ...string) (string, error) {
				var msg WorkMessage
				if len(args) != 2 || args[0] != "work-receive" {
					return "", fmt.Errorf("unexpected command %v", args)
				}
				if err := json.Unmarshal([]byte(args[1]), &msg); err != nil {
					return "", err
				}
				return "", f.c.core.WorkReceive(msg)
			}
			f.net.down["c"] = queued
			n := NewLaunch{Parent: "root", Repo: thirdRepo, Prompt: "third-host child", Session: "child", Source: "third"}
			if _, err := f.b.core.StartNew(n); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if queued && len(f.c.launches.starts) != 0 {
				t.Fatal("unreachable host launched")
			}
			f.net.down["c"] = false
			syncWork(t, f)
			if len(f.c.launches.starts) != 1 || len(f.b.launches.starts) != 1 {
				t.Fatalf("wrong launch host: b=%+v c=%+v", f.b.launches.starts, f.c.launches.starts)
			}
			if _, err := f.b.core.StartNew(n); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if len(f.c.launches.starts) != 1 {
				t.Fatal("third host replay relaunched")
			}
			assignments, _ := f.a.core.Store.Assignments(g.ID)
			if len(assignments) != 2 || assignments[1].Session != "c/child" {
				t.Fatalf("origin missed nested host: %+v", assignments)
			}
			if _, err := f.c.core.ReportAssignment("child", assignments[1].ID, "third-host terminal result"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			assignments, _ = f.a.core.Store.Assignments(g.ID)
			if err := f.a.core.AcceptAssignment(m.Session, assignments[1].ID, assignments[1].Report); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			proxy, err := f.c.core.Store.remoteWork(assignments[1].ID)
			if err != nil || proxy.State != "accepted" {
				t.Fatalf("destination acceptance receipt: %+v %v", proxy, err)
			}
		})
	}
}

func TestRemotePRDiscoveryPrecedesTerminalReportAndPreservesTouchedDropped(t *testing.T) {
	for _, mode := range []string{"owned", "touched", "dropped"} {
		t.Run(mode, func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "remote PR", "/repo/app")
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			st := PRStatus{State: "OPEN", Pending: 1, Fetched: f.b.clock}
			if mode == "touched" {
				st.Merge.Author, st.Merge.Viewer = "other", "me"
			}
			if err := f.b.core.Store.SavePRStatus(pr1, st); err != nil {
				t.Fatal(err)
			}
			f.b.set("worker", func(s *Session) { s.PRs = []PR{{URL: pr1}} })
			if mode == "dropped" {
				if _, _, err := f.a.core.DropGoalPR(m.Session, g.ID, pr1, "excluded work"); err != nil {
					t.Fatal(err)
				}
			}
			a, _ := f.a.core.Store.Assignments(g.ID)
			if _, err := f.b.core.ReportAssignment("worker", a[0].ID, "terminal with discovered PR"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			a, _ = f.a.core.Store.Assignments(g.ID)
			if a[0].Report == 0 {
				t.Fatal("terminal report did not route")
			}
			owned, err := f.a.core.Store.GoalPRs()
			if err != nil {
				t.Fatal(err)
			}
			touched, _ := f.a.core.Store.TouchedPRs()
			if (len(owned[g.ID]) == 1) != (mode == "owned") || (len(touched[g.ID]) == 1) != (mode == "touched") {
				t.Fatalf("remote PR ownership %s: owned=%v touched=%v", mode, owned, touched)
			}
			if err := f.a.core.AcceptAssignment(m.Session, a[0].ID, a[0].Report); err != nil {
				t.Fatal(err)
			}
			_, v, err := f.a.core.FinishGoal(m.Session, "complete", nil)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(v.PRSnapshot, pr1) != (mode == "owned") {
				t.Fatalf("completion snapshot lost remote ownership: %+v", v)
			}
		})
	}
}
