package core

import (
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func verifierGoal(t *testing.T, st *Store) Goal {
	t.Helper()
	g, err := st.AddGoal(Goal{Ask: "verify importer", Repo: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func currentVerifier(t *testing.T, st *Store, goal string) Verification {
	t.Helper()
	all, err := st.Verifications(goal)
	if err != nil || len(all) == 0 {
		t.Fatalf("attempts: %v, %v", all, err)
	}
	return all[len(all)-1]
}

func TestVerifierSessionHistorySurvivesLifecycle(t *testing.T) {
	for _, verdict := range []string{"", VerdictPass, VerdictFail, VerdictError} {
		t.Run("verdict="+verdict, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym.db")
			st, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { st.Close() }()
			g := verifierGoal(t, st)
			v, err := st.AddVerification(Verification{Goal: g.ID, Session: "old"})
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]VerifierSession{"old": {Verification: v.ID, Goal: g.ID}}
			for _, session := range []string{"new", "", "retry"} {
				v.Session = session
				if err := st.SaveVerification(v); err != nil {
					t.Fatal(err)
				}
				v = currentVerifier(t, st, g.ID)
				if session != "" {
					want[session] = VerifierSession{Verification: v.ID, Goal: g.ID}
				}
				got, err := st.VerifierSessions()
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatalf("history=%v, %v; want %v", got, err, want)
				}
			}
			if verdict != "" {
				g, err = st.Goal(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = st.commitVerdict(g, v, verdict, []string{"checked"}, time.Now()); err != nil {
					t.Fatal(err)
				}
				v = currentVerifier(t, st, g.ID)
				v.Ended = true
				v.Session = "unaccepted-terminal-session"
				if err = st.SaveVerification(v); err != nil {
					t.Fatal(err)
				}
			}
			if err = st.ResumeGoal(g.ID, "implementation", "new work", time.Now()); err != nil {
				t.Fatal(err)
			}
			second, err := st.AddVerification(Verification{Goal: g.ID, Session: "next-attempt"})
			if err != nil {
				t.Fatal(err)
			}
			want[second.Session] = VerifierSession{Verification: second.ID, Goal: g.ID}
			all, err := st.Verifications(g.ID)
			if err != nil || len(all) != 2 {
				t.Fatalf("attempts=%v, %v", all, err)
			}
			expected := verdict
			if expected == "" {
				expected = VerdictSuperseded
			}
			if all[0].Verdict != expected {
				t.Fatalf("history verdict=%s, want %s", all[0].Verdict, expected)
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			got, err := st.VerifierSessions()
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("restarted history=%v, %v; want %v", got, err, want)
			}
		})
	}
}

func TestVerifierHistoryBackfillsAvailableRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]VerifierSession{}
	for _, tc := range []struct{ session, verdict string }{{"running", ""}, {"passed", VerdictPass}, {"failed", VerdictFail}, {"", VerdictError}} {
		g := verifierGoal(t, st)
		v, err := st.AddVerification(Verification{Goal: g.ID, Session: tc.session})
		if err != nil {
			t.Fatal(err)
		}
		if tc.verdict != "" {
			g, err = st.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.commitVerdict(g, v, tc.verdict, []string{"checked"}, time.Now()); err != nil {
				t.Fatal(err)
			}
			v = currentVerifier(t, st, g.ID)
			v.Ended = true
			if err = st.SaveVerification(v); err != nil {
				t.Fatal(err)
			}
		}
		if tc.session != "" {
			want[tc.session] = VerifierSession{Verification: v.ID, Goal: g.ID}
		}
	}
	if _, err = st.db.Exec(`DROP TABLE ym_verifier_sessions`); err != nil {
		t.Fatal(err)
	}
	if err = st.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		st, err = OpenStore(path)
		if err != nil {
			t.Fatal(err)
		}
		got, err := st.VerifierSessions()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("backfill=%v, %v; want %v", got, err, want)
		}
		if err = st.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVerifierBindingAndCurrentSessionCommitTogether(t *testing.T) {
	for _, operation := range []string{"save", "prepare", "open"} {
		t.Run(operation, func(t *testing.T) {
			w := newWorld(t)
			st := w.core.Store
			g := verifierGoal(t, st)
			v, err := st.AddVerification(Verification{Goal: g.ID, Session: "old"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := st.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = st.db.Exec(`CREATE TRIGGER reject_verifier BEFORE INSERT ON ym_verifier_sessions WHEN NEW.session='new' BEGIN SELECT RAISE(ABORT,'binding failed'); END`); err != nil {
				t.Fatal(err)
			}
			v.Session = "new"
			switch operation {
			case "save":
				err = st.SaveVerification(v)
			case "prepare":
				_, err = st.prepareVerifier(v)
			case "open":
				_, err = st.AddVerification(Verification{Goal: g.ID, Session: "new"})
			}
			if err == nil {
				t.Fatal("write succeeded despite rejected binding")
			}
			all, err := st.Verifications(g.ID)
			if err != nil || len(all) != 1 || all[0].Session != "old" || all[0].Verdict != "" || all[0].Version != 0 {
				t.Fatalf("attempt changed: %v, %v", all, err)
			}
			after, err := st.Goal(g.ID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("goal changed: %+v, %v", after, err)
			}
			got, err := st.VerifierSessions()
			if err != nil || len(got) != 1 || got["old"].Verification != v.ID {
				t.Fatalf("history changed: %v, %v", got, err)
			}
		})
	}
}

func TestVerifierRejectedWritersRollBackHistory(t *testing.T) {
	for _, operation := range []string{"save", "prepare"} {
		for _, reason := range []string{"version", "work", "active claim"} {
			t.Run(operation+"/"+reason, func(t *testing.T) {
				st := newWorld(t).core.Store
				g := verifierGoal(t, st)
				stale, err := st.AddVerification(Verification{Goal: g.ID, Session: "stored"})
				if err != nil {
					t.Fatal(err)
				}
				switch reason {
				case "version":
					if err = st.SaveVerification(stale); err != nil {
						t.Fatal(err)
					}
				case "work":
					if err = st.ResumeGoal(g.ID, "new-work", "implementation", time.Now()); err != nil {
						t.Fatal(err)
					}
				case "active claim":
					if _, err = st.AddVerification(Verification{Goal: g.ID}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = st.db.Exec(`DELETE FROM ym_verifier_sessions`); err != nil {
					t.Fatal(err)
				}
				stale.Session = "rejected"
				if operation == "save" {
					err = st.SaveVerification(stale)
				} else {
					_, err = st.prepareVerifier(stale)
				}
				if !errors.Is(err, ErrStale) {
					t.Fatalf("stale writer: %v", err)
				}
				got, err := st.VerifierSessions()
				if err != nil || len(got) != 0 {
					t.Fatalf("rejected transaction left history: %v, %v", got, err)
				}
			})
		}
	}
}

func TestVerifierPreparedIdentitySurvivesLaunchOutcome(t *testing.T) {
	for _, outcome := range []string{"actual", "failure", "superseded during launch"} {
		t.Run(outcome, func(t *testing.T) {
			w := newVerifyWorld(t)
			m := w.manager
			m.Harness = "codex"
			if err := w.core.Store.SaveManager(m); err != nil {
				t.Fatal(err)
			}
			w.manager = m
			v := w.done(t, "checked importer")
			var replacement Verification
			switch outcome {
			case "failure":
				w.launches.fail = errors.New("launch failed")
			case "superseded during launch":
				w.launches.during = func() { replacement = w.done(t, "replacement claim") }
			}
			w.clock = w.clock.Add(time.Second)
			err := w.core.Loop(w.board(t))
			if outcome == "superseded during launch" {
				if !errors.Is(err, ErrStale) {
					t.Fatalf("stale launch: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			prepared := w.launches.starts[len(w.launches.starts)-1].Session
			if prepared == "" {
				t.Fatal("launch missing prepared identity")
			}
			history, err := w.core.Store.VerifierSessions()
			if err != nil || history[prepared].Verification != v.ID {
				t.Fatalf("prepared identity lost: %v, %v", history, err)
			}
			current := currentVerifier(t, w.core.Store, v.Goal)
			if outcome == "actual" {
				if current.Session == "" || current.Session == prepared || history[current.Session].Verification != v.ID || len(history) != 2 {
					t.Fatalf("actual identity lost: current=%+v history=%v", current, history)
				}
			} else if outcome == "failure" {
				if current.Session != "" || len(history) != 1 {
					t.Fatalf("failed launch history: %+v, %v", current, history)
				}
			} else if current.ID != replacement.ID || len(history) != 1 {
				t.Fatalf("superseding history: %+v, %v", current, history)
			}
		})
	}
}

func TestVerifierPreparedTransitionsKeepFirstBinding(t *testing.T) {
	for _, actual := range []string{"actual", ""} {
		t.Run("actual="+actual, func(t *testing.T) {
			st := newWorld(t).core.Store
			g := verifierGoal(t, st)
			v, err := st.AddVerification(Verification{Goal: g.ID, Session: "stored"})
			if err != nil {
				t.Fatal(err)
			}
			v.Session = "prepared"
			v, err = st.prepareVerifier(v)
			if err != nil {
				t.Fatal(err)
			}
			v.Session = actual
			if err = st.SaveVerification(v); err != nil {
				t.Fatal(err)
			}
			second, err := st.AddVerification(Verification{Goal: g.ID, Session: "prepared"})
			if err != nil {
				t.Fatal(err)
			}
			history, err := st.VerifierSessions()
			want := map[string]VerifierSession{
				"stored":   {Verification: v.ID, Goal: g.ID},
				"prepared": {Verification: v.ID, Goal: g.ID},
			}
			if actual != "" {
				want[actual] = VerifierSession{Verification: v.ID, Goal: g.ID}
			}
			if err != nil || !reflect.DeepEqual(history, want) {
				t.Fatalf("first binding changed after attempt %d: %v, %v; want %v", second.ID, history, err, want)
			}
		})
	}
}

func TestVerifierBoardRoleIsSeparateFromRoutingAndWorkers(t *testing.T) {
	for _, state := range []string{GoalVerifying, GoalDone, GoalActive} {
		for _, managerLive := range []bool{true, false} {
			for _, ended := range []bool{false, true} {
				t.Run(state+"/manager="+boolName(managerLive)+"/ended="+boolName(ended), func(t *testing.T) {
					w := newOrgWorld(t)
					g, m := w.goal(t, "verify the importer", "/repo/yamnaya")
					w.spawn("ordinary-owner", "")
					w.spawn("891caa221ff5", m.Session)
					for _, id := range []string{"old-verifier", "new-verifier"} {
						w.spawn(id, "")
					}
					v, err := w.core.Store.AddVerification(Verification{Goal: g.ID, Session: "old-verifier"})
					if err != nil {
						t.Fatal(err)
					}
					v.Session = "new-verifier"
					if err := w.core.Store.SaveVerification(v); err != nil {
						t.Fatal(err)
					}
					v = currentVerifier(t, w.core.Store, g.ID)
					v.Session = ""
					if err := w.core.Store.SaveVerification(v); err != nil {
						t.Fatal(err)
					}
					v = currentVerifier(t, w.core.Store, g.ID)
					if ended {
						g, err = w.core.Store.Goal(g.ID)
						if err != nil {
							t.Fatal(err)
						}
						if _, err = w.core.Store.commitVerdict(g, v, VerdictPass, nil, w.clock); err != nil {
							t.Fatal(err)
						}
						v = currentVerifier(t, w.core.Store, g.ID)
						v.Ended = true
						if err = w.core.Store.SaveVerification(v); err != nil {
							t.Fatal(err)
						}
					}
					g, err = w.core.Store.Goal(g.ID)
					if err != nil {
						t.Fatal(err)
					}
					g.State, g.DoneAt = state, w.clock
					if err = w.core.Store.SaveGoal(g); err != nil {
						t.Fatal(err)
					}
					if !managerLive {
						w.stop(m.Session)
					}
					b := w.board(t)
					n, ok := b.Node(m.ID)
					if !ok {
						t.Fatal("goal missing")
					}
					if len(n.Verifiers) != 2 {
						t.Fatalf("verifiers=%v", n.Verifiers)
					}
					for _, id := range []string{"old-verifier", "new-verifier"} {
						p, ok := b.Project(id)
						if !ok || p.Key != id || p.Manager != m.ID || p.Verifier == nil || p.Verifier.Goal != g.ID || p.Verifier.Verification != v.ID {
							t.Fatalf("project=%+v", p)
						}
						if b.parents[id] != "" || b.ProjectOf(id) != id {
							t.Fatalf("verifier ancestry changed: %v", b.parents)
						}
						if _, ok := b.Org.ManagerOf(id); ok {
							t.Fatal("verifier became manager identity")
						}
						if _, ok := b.Org.Managed(id, b.parents); ok {
							t.Fatal("verifier became managed worker")
						}
						if b.Org.Recipient(id, b.parents) != Owner {
							t.Fatal("verifier routing changed")
						}
						for _, a := range n.Agents {
							if a.Key() == id {
								t.Fatal("verifier counted as agent")
							}
						}
						for _, a := range n.Workers {
							if a.Session == id {
								t.Fatal("verifier counted as worker")
							}
						}
					}
					if p, ok := b.Project("ordinary-owner"); !ok || p.Manager != "" || p.Verifier != nil {
						t.Fatalf("owner affected: %+v", p)
					}
					if b.parents["891caa221ff5"] != m.Session || b.ProjectOf("891caa221ff5") != m.Session {
						t.Fatal("worker ancestry changed")
					}
					if p, ok := b.Project(m.Session); !ok || p.Manager != m.ID || p.Verifier != nil {
						t.Fatalf("worker project=%+v", p)
					}
					wantWorkers := 1
					if state == GoalDone {
						wantWorkers = 0
					}
					if len(n.Workers) != wantWorkers || n.Stats.Running+n.Stats.Idle != wantWorkers {
						t.Fatalf("ordinary worker counts changed: workers=%v stats=%+v", n.Workers, n.Stats)
					}
				})
			}
		}
	}
}

func boolName(b bool) string {
	if b {
		return "present"
	}
	return "absent"
}

type changingVerifierHarness struct{ *fakeHarness }

func (changingVerifierHarness) Generation() uint64 { return 1 }

func TestVerifierAssociationSurvivesBoardReuseAndRefresh(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "verify the importer", "/repo/yamnaya")
	w.spawn("verifier", "")
	w.core.Harnesses = []Harness{changingVerifierHarness{w.claude}, changingVerifierHarness{w.codex}}
	before := w.board(t)
	v, err := w.core.Store.AddVerification(Verification{Goal: g.ID, Session: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	if b.Gen == before.Gen {
		t.Fatal("binding write reused old board")
	}
	again := w.board(t)
	if again.Gen != b.Gen {
		t.Fatalf("unchanged board rebuilt: %d -> %d", b.Gen, again.Gen)
	}
	p, ok := again.Project("verifier")
	if !ok || p.Verifier == nil || p.Manager != m.ID {
		t.Fatalf("reused project=%+v", p)
	}
	v.Session = "next-verifier"
	if err := w.core.Store.SaveVerification(v); err != nil {
		t.Fatal(err)
	}
	next := w.board(t)
	if next.Gen == b.Gen || len(next.VerifierSessions) != 2 || len(b.VerifierSessions) != 1 {
		t.Fatalf("board refresh lost snapshot isolation: old=%v new=%v", b.VerifierSessions, next.VerifierSessions)
	}
	w.clock = w.clock.Add(time.Second)
	if n, ok := next.Node(m.ID); !ok || len(n.Verifiers) != 1 {
		t.Fatalf("refreshed node=%+v", n)
	}
}

func TestVerifierOwnershipIsIsolatedByHostWithIdenticalUUIDs(t *testing.T) {
	for _, tc := range []struct {
		name          string
		local, remote bool
	}{
		{"local only", true, false}, {"remote only", false, true}, {"both", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "local goal", "/repo/yamnaya")
			const id = "01a18f3a-9518-7283-ba01-2842bf82ab77"
			w.spawn(id, "")
			if tc.local {
				if _, err := w.core.Store.AddVerification(Verification{Goal: g.ID, Session: id}); err != nil {
					t.Fatal(err)
				}
			}
			remote := RemoteBoard{Host: "box", At: w.clock,
				Sessions: []Live{{Session: Session{Harness: "claude", ID: id, Status: Busy}, Project: id, Remote: "box"}},
				Managers: []Manager{{ID: "mgr1", GoalID: "goal46", Role: RoleManager, Session: "far-manager"}},
				Goals:    []Goal{{ID: "goal46", ManagerID: "mgr1", State: GoalVerifying}},
			}
			if tc.remote {
				remote.VerifierSessions = map[string]VerifierSession{id: {Verification: 20, Goal: "goal46"}}
			}
			remotes := &fakeRemotes{boards: []RemoteBoard{remote}}
			w.core.Remotes, w.core.ShowRemotes = remotes, true
			b := w.board(t)
			found := map[string]bool{}
			for _, p := range b.Projects {
				if p.Key != id {
					continue
				}
				if len(p.Sessions) != 1 {
					t.Fatalf("hosts merged: %+v", p)
				}
				host := p.Sessions[0].Remote
				wantRole, wantManager := tc.local, m.ID
				if host != "" {
					wantRole, wantManager = tc.remote, "box/mgr1"
				}
				found[host] = true
				if (p.Verifier != nil) != wantRole {
					t.Fatalf("host %q role=%+v want %v", host, p.Verifier, wantRole)
				}
				if wantRole && p.Manager != wantManager || !wantRole && p.Manager != "" {
					t.Fatalf("host %q manager=%q", host, p.Manager)
				}
				_, bound := b.Verifier(p.Sessions[0])
				if bound != wantRole {
					t.Fatal("host lookup leaked verifier identity")
				}
			}
			if len(found) != 2 {
				t.Fatalf("lost colliding session: %v", found)
			}
			for manager, want := range map[string]bool{m.ID: tc.local, "box/mgr1": tc.remote} {
				n, ok := b.Node(manager)
				if !ok {
					t.Fatalf("missing node %s", manager)
				}
				count := 0
				if want {
					count = 1
				}
				if len(n.Verifiers) != count || len(n.Agents) != 0 || len(n.Workers) != 0 {
					t.Fatalf("host role leaked into node %s: %+v", manager, n)
				}
			}
		})
	}
}

func TestVerifierUUIDCollisionPreservesEachHostsProjectAncestry(t *testing.T) {
	for _, remoteVerifier := range []bool{true, false} {
		t.Run(boolName(remoteVerifier), func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "local goal", "/repo/yamnaya")
			const id = "shared-session-uuid"
			parent := ""
			if remoteVerifier {
				parent = m.Session
			}
			w.spawn(id, parent)
			r := RemoteBoard{Host: "box", At: w.clock, Managers: []Manager{{ID: "mgr1", GoalID: "goal46", Role: RoleManager, Session: "far-manager"}}, Goals: []Goal{{ID: "goal46", ManagerID: "mgr1", State: GoalVerifying}}}
			remoteProject := "far-manager"
			if remoteVerifier {
				remoteProject = id
				r.VerifierSessions = map[string]VerifierSession{id: {Goal: "goal46", Verification: 20}}
			} else {
				if _, err := w.core.Store.AddVerification(Verification{Goal: g.ID, Session: id}); err != nil {
					t.Fatal(err)
				}
			}
			r.Sessions = []Live{{Session: Session{ID: id, Harness: "claude", Status: Busy}, Remote: "box", Project: remoteProject}}
			w.core.Remotes, w.core.ShowRemotes = &fakeRemotes{boards: []RemoteBoard{r}}, true
			b := w.board(t)
			for _, s := range b.Sessions {
				if s.Key() != id {
					continue
				}
				want := id
				if s.Remote != "" {
					want = remoteProject
				} else if remoteVerifier {
					want = m.Session
				}
				if s.Project != want {
					t.Fatalf("host %q project=%q want %q", s.Remote, s.Project, want)
				}
				_, verifier := b.Verifier(s)
				if verifier != (s.Remote != "" == remoteVerifier) {
					t.Fatalf("role crossed hosts: %+v", s)
				}
			}
			if b.parents[id] != parent {
				t.Fatalf("local ancestry changed: %v", b.parents)
			}
		})
	}
}

func TestDormantRemoteAncestryDoesNotReparentLocalVerifier(t *testing.T) {
	for _, parent := range []string{"", "remote-parent"} {
		t.Run(parent, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "local goal", "/repo/yamnaya")
			const id = "colliding-uuid"
			w.spawn(id, "")
			if _, err := w.core.Store.AddVerification(Verification{Goal: g.ID, Session: id}); err != nil {
				t.Fatal(err)
			}
			r := RemoteBoard{Host: "box", At: w.clock, Dormant: []Dormant{{Key: "box/worker", Session: id, Parent: parent}}}
			w.core.Remotes, w.core.ShowRemotes = &fakeRemotes{boards: []RemoteBoard{r}}, true
			b := w.board(t)
			p, ok := b.Project(id)
			if !ok || p.Manager != m.ID || p.Verifier == nil || len(p.Sessions) != 1 || len(p.Dormant) != 0 {
				t.Fatalf("local verifier changed by remote launch: %+v", p)
			}
			if b.ProjectOf(id) != id || b.parents[id] != "" {
				t.Fatalf("remote parent leaked into local ancestry: %v", b.parents)
			}
			remoteKey := id
			if parent != "" {
				remoteKey = parent
			}
			found := false
			for _, p := range b.Projects {
				if len(p.Dormant) == 0 {
					continue
				}
				found = true
				if p.Key != remoteKey || p.Verifier != nil || p.Manager != "" || ProjectHost(p) != "box" {
					t.Fatalf("remote worker misclassified: %+v", p)
				}
			}
			if !found {
				t.Fatal("remote dormant worker disappeared")
			}
		})
	}
}

type alternatingVerifierRemotes struct {
	fakeRemotes
	next RemoteBoard
}

func (r *alternatingVerifierRemotes) Boards() []RemoteBoard {
	current := r.boards
	r.boards = []RemoteBoard{r.next}
	return current
}

func TestVerifierSessionsAndOwnershipUseTheSameRemoteGeneration(t *testing.T) {
	w := newOrgWorld(t)
	r := &alternatingVerifierRemotes{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{
		Host: "box", At: w.clock,
		Sessions:         []Live{{Session: Session{Harness: "claude", ID: "verifier", Status: Busy}, Project: "verifier"}},
		Managers:         []Manager{{ID: "mgr1", GoalID: "goal1", Role: RoleManager, Session: "manager"}},
		Goals:            []Goal{{ID: "goal1", ManagerID: "mgr1", State: GoalVerifying}},
		VerifierSessions: map[string]VerifierSession{"verifier": {Verification: 1, Goal: "goal1"}},
	}}}, next: RemoteBoard{Host: "box", At: w.clock.Add(time.Second)}}
	w.core.Remotes, w.core.ShowRemotes = r, true
	b := w.board(t)
	p, ok := b.Project("verifier")
	if !ok || p.Verifier == nil || p.Manager != "box/mgr1" {
		t.Fatalf("mixed generations lost ownership: %+v", p)
	}
	n, ok := b.Node("box/mgr1")
	if !ok || len(n.Verifiers) != 1 {
		t.Fatalf("mixed generations lost verifier detail: %+v", n)
	}
	next := w.board(t)
	if _, ok := next.Project("verifier"); ok || len(next.VerifierSessions) != 0 {
		t.Fatalf("remote refresh retained previous generation: %+v", next.Projects)
	}
}

func TestRemoteManagerMembershipDoesNotOwnCollidingLocalSession(t *testing.T) {
	w := newOrgWorld(t)
	const id = "shared-manager-uuid"
	w.spawn(id, "")
	r := RemoteBoard{Host: "box", At: w.clock,
		Sessions:        []Live{{Session: Session{Harness: "claude", ID: id}, Project: id}},
		Managers:        []Manager{{ID: "mgr1", GoalID: "goal1", Role: RoleManager, Session: id}},
		Goals:           []Goal{{ID: "goal1", ManagerID: "mgr1", State: GoalActive}},
		ManagerSessions: map[string]string{id: "mgr1"},
	}
	w.core.Remotes, w.core.ShowRemotes = &fakeRemotes{boards: []RemoteBoard{r}}, true
	b := w.board(t)
	for _, p := range b.Projects {
		want := ""
		if ProjectHost(p) == "box" {
			want = "box/mgr1"
		}
		if p.Manager != want || p.Verifier != nil {
			t.Fatalf("manager ownership crossed hosts: %+v", p)
		}
	}
	n, ok := b.Node("box/mgr1")
	if !ok || n.Live == nil || n.Live.Remote != "box" || len(n.Agents) != 0 {
		t.Fatalf("manager resolved another host's session: %+v", n)
	}
}
