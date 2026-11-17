package core

import (
	"strings"
	"testing"
	"time"
)

func TestSupervisorAttachmentsPreserveExecutionOwnership(t *testing.T) {
	w := newOrgWorld(t)
	chief := w.chief(t, "delivery")
	_, manager := w.goal(t, "ship exporter", "/repo/export")
	if err := w.core.Attach(manager.ID, chief.ID); err != nil {
		t.Fatal(err)
	}
	s, err := w.core.NewSupervisor("intent", "Ship export; no merges.", "", "", []string{chief.ID, manager.ID})
	if err != nil {
		t.Fatal(err)
	}
	o, _ := w.core.LoadOrg()
	if o.Managers[manager.ID].ParentID != chief.ID || o.Managers[chief.ID].ParentID != "" || o.Managers[s.ID].ParentID != "" {
		t.Fatal("supervision changed execution ownership")
	}
	view, err := w.core.Supervisor(s.ID)
	if err != nil || len(view.Targets) != 2 || len(view.Evidence) != 2 {
		t.Fatalf("view %+v, %v", view, err)
	}
	if !w.core.supervises(s.ID, manager.ID) {
		t.Fatal("supervisor cannot inspect chief's report")
	}
	if _, err := w.core.SteerContext(w.board(t), manager.ID, "Retain backward compatibility.", s.ID); err != nil {
		t.Fatal(err)
	}
	_, stranger := w.goal(t, "unrelated", "/repo/other")
	if _, err := w.core.SteerContext(w.board(t), stranger.ID, "Do other work.", s.ID); err == nil {
		t.Fatal("supervisor steered unattached work")
	}
	if _, err := w.core.CloseAuthor(s.Session, ""); err == nil {
		t.Fatal("supervisor gained chief close authority")
	}
	if err := w.core.SupervisorAttach(s.ID, chief.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := w.core.SupervisorAttach(s.ID, manager.ID, true); err != nil {
		t.Fatal(err)
	}
	if w.core.supervises(s.ID, manager.ID) {
		t.Fatal("detached supervisor retains steering authority")
	}
}

func TestSupervisorIntentReportsAndCorrectionsSurviveRelaunch(t *testing.T) {
	w := newOrgWorld(t)
	s, err := w.core.NewSupervisor("intent", "Preserve the original API.", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := w.core.SteerContext(w.board(t), s.ID, "Preserve the original API. Also support CSV.", Owner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.WriteBrief(s.Session, Brief{Assessment: "Evidence incomplete", Changed: "Read diff", Uncertainty: "Tests unavailable", Next: "Request evidence", WaitingOn: "manager"}); err != nil {
		t.Fatal(err)
	}
	view, err := w.core.Supervisor(s.ID)
	if err != nil || view.OriginalIntent != s.Remit || view.Revision != st.Revision || len(view.Reports) != 1 || len(view.Corrections) != 1 {
		t.Fatalf("view %+v, %v", view, err)
	}
	w.stop(s.Session)
	o, _ := w.core.LoadOrg()
	w.core.relaunch(w.board(t), o, o.Managers[s.ID])
	prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
	for _, want := range []string{s.Remit, "Also support CSV", "supervisor", "Do not implement", "Other agents keep working"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt lost %q: %s", want, prompt)
		}
	}
}

func TestSupervisorEvidenceActionsAreDurableAndOnlyFollowChanges(t *testing.T) {
	w := newOrgWorld(t)
	_, target := w.goal(t, "ship", "/repo/app")
	s, err := w.core.NewSupervisor("intent", "Ship without scope expansion.", "", "", []string{target.ID})
	if err != nil {
		t.Fatal(err)
	}
	watch := func() {
		t.Helper()
		o, _ := w.core.LoadOrg()
		if err := w.core.watchSupervisors(w.board(t), o); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		actions, err := w.core.Store.Activations()
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range actions {
			if a.Recipient == s.ID && a.Kind == "supervise" {
				n++
			}
		}
		return n
	}
	watch()
	watch()
	if n := count(); n != 1 {
		t.Fatalf("unchanged evidence generated %d actions", n)
	}
	if _, err := w.core.WriteBrief(target.Session, Brief{Assessment: "Ready", Changed: "Tests pass", Uncertainty: "none", Next: "review", WaitingOn: "none"}); err != nil {
		t.Fatal(err)
	}
	watch()
	watch()
	if n := count(); n != 2 {
		t.Fatalf("changed evidence generated %d actions", n)
	}
	w.stop(s.Session)
	watch()
	if n := count(); n != 2 {
		t.Fatalf("absent supervisor generated actions: %d", n)
	}
}

func TestSupervisorRemoteEvidenceNeverConflatesFreshAndStale(t *testing.T) {
	w := newOrgWorld(t)
	remote := &fakeRemotes{boards: []RemoteBoard{{Host: "box", At: w.clock, Managers: []Manager{{ID: "mgr1", Name: "remote", Role: RoleManager, GoalID: "goal1"}}, Goals: []Goal{{ID: "goal1", ManagerID: "mgr1", Ask: "Remote work"}}, Standing: map[string]string{"mgr1": StandMoving}, Feeds: map[string]Feed{"goal1": {Latest: &GoalEvent{Seq: 7, Goal: "goal1", Kind: EventCIGreen}}}}}}
	w.core.Remotes, w.core.ShowRemotes = remote, false
	s, err := w.core.NewSupervisor("intent", "Check remote evidence.", "", "", []string{"box/mgr1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		stale       bool
		age         time.Duration
		unavailable bool
	}{{"fresh", false, 0, false}, {"retained", true, 0, true}, {"behind", false, SnapshotLate + time.Second, true}} {
		t.Run(tc.name, func(t *testing.T) {
			remote.boards[0].Stale = tc.stale
			remote.boards[0].At = w.clock.Add(-tc.age)
			view, err := w.core.Supervisor(s.ID)
			if err != nil || len(view.Evidence) != 1 {
				t.Fatalf("view %+v, %v", view, err)
			}
			if view.Evidence[0].Latest == nil || view.Evidence[0].Latest.Seq != 7 {
				t.Fatal("remote goal evidence was lost after host qualification")
			}
			if (view.Evidence[0].Unavailable != "") != tc.unavailable {
				t.Fatalf("evidence %+v", view.Evidence)
			}
		})
	}
	if _, err := w.core.KillGoal(w.board(t), s.ID); err != nil {
		t.Fatal(err)
	}
	if remote.boards[0].Managers[0].Retired {
		t.Fatal("retiring supervisor affected remote execution")
	}
}

func TestSupervisorRemoteSteerUsesAttachmentVouchWithoutReparenting(t *testing.T) {
	f := newFleet(t)
	_, m := f.b.goal(t, "ship", "/repo/app")
	s, err := f.a.core.NewSupervisor("intent", "Keep the scope.", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.a.core.Store.db.Exec(`INSERT INTO ym_supervisor_links VALUES (?,?)`, s.ID, RemoteKey("b", m.ID)); err != nil {
		t.Fatal(err)
	}
	st, err := f.b.core.SteerContext(f.b.board(t), m.ID, "Keep compatibility.", RemoteKey("a", s.ID))
	if err != nil || st.Pending != RemoteKey("a", s.ID) {
		t.Fatalf("pending %+v, %v", st, err)
	}
	for range 3 {
		f.sync(t)
	}
	steers, err := f.b.core.Store.Steers(m.ID)
	if err != nil || len(steers) != 1 || steers[0].Author != RemoteKey("a", s.ID) {
		t.Fatalf("steers %+v, %v", steers, err)
	}
	o, _ := f.b.core.LoadOrg()
	if !strings.HasPrefix(o.SteerBy(steers[0]), "supervisor ") {
		t.Fatal("remote supervisor was mislabeled as a chief")
	}
	actual, _ := f.b.core.Store.Manager(m.ID)
	if actual.ParentID != "" {
		t.Fatal("vouch reparented target")
	}
	if _, err := f.a.core.Store.db.Exec(`DELETE FROM ym_supervisor_links WHERE supervisor=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.core.SteerContext(f.b.board(t), m.ID, "Unauthorized revision.", RemoteKey("a", s.ID)); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		f.sync(t)
	}
	steers, _ = f.b.core.Store.Steers(m.ID)
	if len(steers) != 1 {
		t.Fatal("detached supervisor's remote steer landed")
	}
}

func TestSupervisorReportsBindPrimarySourcesAndObservedContext(t *testing.T) {
	w := newOrgWorld(t)
	_, target := w.goal(t, "ship", "/repo/app")
	s, err := w.core.NewSupervisor("intent", "Preserve APIs.", "", "", []string{target.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.RememberSupervision(s.Session, s.ID, "Also preserve CSV.", "conversation/message-2"); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, session string
		sources       []string
		wantErr       bool
	}{{"valid", s.Session, []string{"commit:abc", "test:unit-results"}, false}, {"foreign", target.Session, []string{"commit:abc"}, true}, {"empty-identity", "", []string{"commit:abc"}, true}, {"missing-evidence", s.Session, nil, true}} {
		t.Run(tc.name, func(t *testing.T) {
			report, err := w.core.ReportSupervision(tc.session, s.ID, "API preserved", "integration tests unavailable", tc.sources)
			if (err != nil) != tc.wantErr {
				t.Fatalf("report %+v, %v", report, err)
			}
			if err == nil && (report.ContextRevision != 1 || len(report.Observed) != 1 || report.Session != s.Session) {
				t.Fatalf("report %+v", report)
			}
		})
	}
	view, err := w.core.Supervisor(s.ID)
	if err != nil || len(view.Assessments) != 1 || view.Corrections[0].Author != s.ID || !strings.Contains(view.Context, "conversation/message-2") {
		t.Fatalf("view %+v, %v", view, err)
	}
}

func TestSupervisorCannotCreateExecutionOrJoinReportingHierarchy(t *testing.T) {
	w := newOrgWorld(t)
	s, err := w.core.NewSupervisor("intent", "Watch scope.", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	chief := w.chief(t, "delivery")
	if err := w.core.Attach(s.ID, chief.ID); err == nil {
		t.Fatal("supervisor joined execution hierarchy")
	}
	for _, tc := range []struct {
		name string
		run  func() error
	}{
		{"worker", func() error { _, err := w.core.StartNew(NewLaunch{Parent: s.Session, Prompt: "implement"}); return err }},
		{"goal", func() error { _, _, err := w.core.NewGoal("implement", "/repo/app", ""); return err }},
		{"chief", func() error { _, err := w.core.NewChief("other", "implement", "", "", nil); return err }},
		{"supervisor", func() error { _, err := w.core.NewSupervisor("other", "delegate", "", "", nil); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w.core.Requester = func() string { return s.Session }
			if err := tc.run(); err == nil {
				t.Fatal("supervisor delegated execution")
			}
		})
	}
}

func TestSupervisorSignalsIgnoreBookkeepingButNoticeEvidenceReversions(t *testing.T) {
	w := newOrgWorld(t)
	_, target := w.goal(t, "ship", "/repo/app")
	s, err := w.core.NewSupervisor("intent", "Preserve scope.", "", "", []string{target.ID})
	if err != nil {
		t.Fatal(err)
	}
	watch := func() {
		t.Helper()
		o, _ := w.core.LoadOrg()
		if err := w.core.watchSupervisors(w.board(t), o); err != nil {
			t.Fatal(err)
		}
	}
	count := func() int {
		a, _ := w.core.Store.Activations()
		n := 0
		for _, action := range a {
			if action.Kind == "supervise" {
				n++
			}
		}
		return n
	}
	watch()
	g, err := w.core.GoalByRef(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	g.AckedAt = w.clock
	if err := w.core.Store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.core.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventNudge, Payload: "bookkeeping", Key: "nudge", Created: w.clock}); err != nil {
		t.Fatal(err)
	}
	watch()
	if n := count(); n != 1 {
		t.Fatalf("bookkeeping prompted supervisor %s: %d", s.ID, n)
	}
	g, _ = w.core.GoalByRef(target.ID)
	g.State = GoalVerifying
	if err := w.core.Store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	watch()
	g, _ = w.core.GoalByRef(target.ID)
	g.State = GoalActive
	if err := w.core.Store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	watch()
	if n := count(); n != 3 {
		t.Fatalf("evidence reversal lost: %d actions", n)
	}
}

func TestSupervisorRemoteVouchDoesNotBorrowChiefOrUnknownIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		role   string
		attach bool
		want   int
	}{{"supervisor", RoleSupervisor, true, 1}, {"detached", RoleSupervisor, false, 0}, {"chief-cannot-impersonate", RoleChief, true, 0}, {"unknown", "", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "delivery")
			_, target := f.b.goal(t, "ship", "/repo/app")
			if err := f.b.core.Attach(target.ID, chief.ID); err != nil {
				t.Fatal(err)
			}
			id := "mgr99"
			if tc.role != "" {
				m, err := f.a.core.Store.AddManager(Manager{Role: tc.role, Name: "observer"})
				if err != nil {
					t.Fatal(err)
				}
				id = m.ID
			}
			if tc.attach {
				if _, err := f.a.core.Store.db.Exec(`INSERT INTO ym_supervisor_links VALUES (?,?)`, id, RemoteKey("b", target.ID)); err != nil {
					t.Fatal(err)
				}
			}
			st, err := f.b.core.SteerContextAs(f.b.board(t), target.ID, "Keep compatibility.", RemoteKey("a", id), "observer", true)
			if err != nil || st.Pending != RemoteKey("a", id) {
				t.Fatalf("steer %+v,%v", st, err)
			}
			for range 3 {
				f.sync(t)
			}
			steers, err := f.b.core.Store.Steers(target.ID)
			if err != nil || len(steers) != tc.want {
				t.Fatalf("steers %+v,%v", steers, err)
			}
		})
	}
}
