package core

import (
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"
)

func remoteAgentNews(t *testing.T) (*fleet, Goal, Manager) {
	t.Helper()
	f, repo := remoteWorkFleet(t)
	g, m := f.a.goal(t, "remote work", "/repo/app")
	if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	f.b.spawn("worker", "")
	return f, g, m
}

func TestWhatHappensToARemoteAgentReachesItsOriginManager(t *testing.T) {
	for _, tc := range []struct {
		name, kind, says string
		wakes            bool
		do               func(f *fleet)
	}{
		{"an idle reply", EventFinished, "Pushed the parser", false, func(f *fleet) {
			f.b.set("worker", func(s *Session) { s.Result, s.ResultAt = "Pushed the parser", f.b.clock })
		}},
		{"a hang", EventAgentHung, "ym open b/worker", true, func(f *fleet) {
			f.b.clock = f.b.clock.Add(time.Hour)
			f.b.set("worker", func(s *Session) {
				s.Status, s.Since = Busy, f.b.clock.Add(-45*time.Minute)
				s.Doing = Action{Tool: "Bash", Detail: "go test", At: f.b.clock.Add(-30 * time.Minute)}
			})
		}},
		{"a death", EventAgentStopped, "ym revive b/app-wt-worker", true, func(f *fleet) {
			f.b.stop("worker")
			f.b.launches.launched = []Launched{{Key: "app-wt-worker", Harness: "claude", Session: "worker", State: "exited 1", Path: "/b/app-wt-worker"}}
		}},
		{"a failed resume", EventResumeFailed, "ym revive b/app-wt-worker", true, func(f *fleet) {
			f.b.stop("worker")
			f.b.launches.launched = []Launched{{Key: "app-wt-worker", Harness: "claude", Session: "worker", State: "gone", Revive: true, Path: "/b/app-wt-worker"}}
			f.b.core.Launches = &resumes{fakeLaunches: f.b.launches, fail: map[string]error{"worker": errors.New("no transcript")}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, g, m := remoteAgentNews(t)
			tc.do(f)
			f.b.loop(t, 2)
			syncWork(t, f)
			f.b.loop(t, 1)
			syncWork(t, f)
			events, err := f.a.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got []GoalEvent
			for _, e := range events {
				if e.Kind == tc.kind {
					got = append(got, e)
				}
			}
			if len(got) != 1 || !strings.Contains(got[0].Payload, tc.says) || !strings.Contains(got[0].Payload, "agent b/worker") {
				t.Fatalf("origin recorded %+v, want one %s naming b/worker and saying %q", got, tc.kind, tc.says)
			}
			f.a.clock = f.a.clock.Add(time.Hour)
			f.a.loop(t, 2)
			heard := strings.Join(wakes(f.a.handedTo(m.Session)), "\n")
			if !strings.Contains(heard, tc.kind) || strings.Contains(heard, "for you") != tc.wakes {
				t.Fatalf("manager heard %q, want %s with wakes=%v", heard, tc.kind, tc.wakes)
			}
		})
	}
}

func TestARemoteAgentsRepeatedReplyIsOnlyCountedAndItsWordsStayVerbatim(t *testing.T) {
	f, g, _ := remoteAgentNews(t)
	reply := "The worker pool: ym revive it later"
	for range 2 {
		f.b.clock = f.b.clock.Add(time.Minute)
		f.b.set("worker", func(s *Session) { s.Result, s.ResultAt = reply, f.b.clock })
		f.b.loop(t, 1)
		syncWork(t, f)
	}
	events, err := f.a.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, e := range events {
		got[e.Kind]++
		if e.Kind == EventFinished && !strings.HasSuffix(e.Payload, "verbatim: "+reply) {
			t.Fatalf("the reply changed on its way: %q", e.Payload)
		}
	}
	if got[EventFinished] != 1 || got[EventIdleAgain] != 1 {
		t.Fatalf("origin recorded %v, want one report and one repeat", got)
	}
}

func TestAReportCarriesOnlyPullRequestEventsSinceTheRestAlreadyTravelled(t *testing.T) {
	f, g, _ := remoteAgentNews(t)
	f.b.set("worker", func(s *Session) { s.Result, s.ResultAt = "Pushed the parser", f.b.clock })
	f.b.loop(t, 1)
	assignments, err := f.a.core.Store.Assignments(g.ID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments %+v, %v", assignments, err)
	}
	if _, err := f.b.core.ReportAssignment("worker", assignments[0].ID, "done"); err != nil {
		t.Fatal(err)
	}
	relays, err := f.b.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range relays {
		if strings.Contains(r.Body, `"Op":"report"`) && strings.Contains(r.Body, "Pushed the parser") {
			t.Fatalf("report carries the agent's reply again: %s", r.Body)
		}
	}
	syncWork(t, f)
	if current, _ := f.a.core.Store.Assignments(g.ID); current[0].Report == 0 {
		t.Fatalf("report did not arrive: %+v", current[0])
	}
}

func TestAChiefsOwnAgentsPullRequestNewsReachesIt(t *testing.T) {
	url := "https://github.com/acme/august/pull/7"
	for _, tc := range []struct {
		name string
		st   PRStatus
		kind string
	}{
		{"CI red", PRStatus{State: "OPEN", Failed: 1, Head: "h"}, EventCIRed},
		{"a conflict", PRStatus{State: "OPEN", Head: "h", Merge: PRMergeFacts{OID: "o", Mergeable: "CONFLICTING"}}, EventConflict},
		{"review comments", PRStatus{State: "OPEN", Head: "h", Threads: 2, Unresolved: 2}, EventReview},
	} {
		for _, gone := range []bool{false, true} {
			name := tc.name
			if gone {
				name += " after the agent ended"
			}
			t.Run(name, func(t *testing.T) {
				w := newOrgWorld(t)
				_, m := w.goal(t, "ship the importer", "/repo/august")
				chief := w.chief(t, "platform", m.ID)
				w.clock = w.clock.Add(time.Minute)
				w.spawn("own", chief.Session)
				opened := tc.st
				if gone {
					opened = PRStatus{State: "OPEN", Head: "h"}
				}
				w.pr(t, "own", url, opened)
				w.loop(t, 1)
				if gone {
					w.stop("own")
					tc.st.Fetched = w.clock
					if err := w.core.Store.SavePRStatus(url, tc.st); err != nil {
						t.Fatal(err)
					}
				}
				w.loop(t, 2)
				w.clock = w.clock.Add(time.Minute)
				w.loop(t, 2)
				events, err := w.core.Store.GoalEvents(chief.ID)
				if err != nil {
					t.Fatal(err)
				}
				var found bool
				for _, e := range events {
					found = found || e.Kind == tc.kind
				}
				if !found {
					t.Fatalf("chief recorded %v, want %s", kinds(events), tc.kind)
				}
				got := digests(w.handedTo(chief.Session))
				if len(got) != 1 || !strings.Contains(got[0], ownAgents) {
					t.Fatalf("chief heard %q, want one digest about its agents", got)
				}
			})
		}
	}
}

func TestAChiefsOwnSessionIsNotOneOfItsAgents(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	w.clock = w.clock.Add(time.Minute)
	w.pr(t, chief.Session, "https://github.com/acme/august/pull/7", PRStatus{State: "OPEN", Failed: 1, Head: "h"})
	w.loop(t, 2)
	events, err := w.core.Store.GoalEvents(chief.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if prKind(e.Kind) {
			t.Fatalf("a report's pull request the chief looked at became its own: %v", kinds(events))
		}
	}
}

func TestADeathInsideItsGraceSurvivesAChiefComingBack(t *testing.T) {
	const death = "agent a1 (work) stopped: its process exited with status 143."
	for _, tc := range []struct {
		name string
		back func(t *testing.T, w *orgWorld, chief Manager) Manager
	}{
		{"ym on", func(t *testing.T, w *orgWorld, chief Manager) Manager {
			if _, err := w.core.SetOff(w.board(t), OffTarget{Manager: &chief}, true); err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.SetOff(w.board(t), OffTarget{Manager: &chief}, false); err != nil {
				t.Fatal(err)
			}
			return chief
		}},
		{"a relaunch", func(t *testing.T, w *orgWorld, chief Manager) Manager {
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			return w.core.relaunch(w.board(t), o, chief)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.clock = w.clock.Add(time.Minute)
			if _, _, err := w.core.RecordGoalEvent(g.ID, EventAgentStopped, death, ""); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Minute)
			chief = tc.back(t, w, chief)
			w.clock = w.clock.Add(deathGrace + time.Minute)
			w.loop(t, 2)
			got := digests(w.handedTo(chief.Session))
			if len(got) != 1 || !strings.Contains(got[0], "agent died") {
				t.Fatalf("chief heard %q, want the death once its grace passed", got)
			}
		})
	}
}

func TestOnlyWhatNeedsAChiefCountsAsAWakeDuePastItsCap(t *testing.T) {
	for _, tc := range []struct {
		name, kind, payload string
		asked               bool
	}{
		{"red CI below", EventCIRed, "CI failed", false},
		{"a review below", EventReview, "2 threads", false},
		{"a first failed verification below", EventFailedVerify, "attempt 1 failed: ci red", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			if err := w.core.Store.SetContextCap(100_000); err != nil {
				t.Fatal(err)
			}
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.clock = w.clock.Add(time.Minute)
			if _, _, err := w.core.RecordGoalEvent(g.ID, tc.kind, tc.payload, ""); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(DigestEvery + time.Minute)
			w.set(chief.Session, func(s *Session) {
				s.Context, s.ContextAt, s.Result, s.ResultAt = 120_000, w.clock, "Looked.", w.clock
			})
			w.loop(t, 1)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			restarted := o.Managers[chief.ID].Session != chief.Session
			asked := strings.Contains(strings.Join(w.handedTo(chief.Session), "\n"), "Record a fresh brief now")
			if (asked || restarted) != tc.asked {
				t.Fatalf("asked=%v restarted=%v, want a restart path=%v", asked, restarted, tc.asked)
			}
		})
	}
}

func TestARestartNeverDropsAnUndeliveredActionPastItsEventCut(t *testing.T) {
	const finding = "attempt 1 failed: the export loses rows"
	for _, tc := range []struct {
		name  string
		chief bool
	}{
		{"manager", false},
		{"chief", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Signal = func(pid int, _ syscall.Signal) error {
				w.stop(w.claude.sessions[pid].ID)
				return nil
			}
			if err := w.core.Store.SetContextCap(100_000); err != nil {
				t.Fatal(err)
			}
			g, m := w.goal(t, "ship the importer", "/repo/august")
			who := m
			if tc.chief {
				who = w.chief(t, "platform", m.ID)
			}
			w.clock = w.clock.Add(time.Minute)
			if _, err := w.core.WriteBrief(who.Session, Brief{Assessment: "b", Changed: "c", Uncertainty: "none", Next: "n", WaitingOn: "none"}); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Minute)
			w.record(t, g.ID, EventFailedVerify, finding)
			for range refreshEvents + 5 {
				w.record(t, g.ID, EventUpdate, "half done")
			}
			w.clock = w.clock.Add(time.Hour)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			s, _ := w.board(t).Session(who.Session)
			if _, err := w.core.refresh(w.board(t), o, o.Managers[who.ID], s, 120_000, false); err != nil {
				t.Fatal(err)
			}
			prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
			if !strings.Contains(prompt, finding) {
				t.Fatalf("the prompt lost the failed verification:\n%s", prompt)
			}
			if got := strings.Count(prompt, "half done"); got != refreshEvents-1 {
				t.Fatalf("prompt holds %d others, want %d:\n%s", got, refreshEvents-1, prompt)
			}
		})
	}
}

func TestYmDigestPrintsWhatNeedsAChiefBeforeItsBatchIsOut(t *testing.T) {
	url := "https://github.com/acme/august/pull/19"
	for _, tc := range []struct {
		name string
		do   func(t *testing.T, w *orgWorld, g Goal, chief Manager)
		want string
	}{
		{"waiting out its batch", func(t *testing.T, w *orgWorld, g Goal, chief Manager) {
			batch := WakePace{Debounce: ChiefBatch, Digest: DigestEvery}
			w.core.Pace = &batch
			w.record(t, g.ID, EventPRReady, `"Importer" `+url+" is green and ready to merge")
			w.record(t, g.ID, EventVerified, "attempt 1 passed")
		}, "PR #19 ready, verified #"},
		{"rolled and held for the next message", func(t *testing.T, w *orgWorld, g Goal, chief Manager) {
			if err := w.core.Store.addDigest(chief.ID, Rollup{g.Ref(): {Actions: []string{"failed verify #9"}}}, nil); err != nil {
				t.Fatal(err)
			}
		}, "failed verify #9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.clock = w.clock.Add(time.Minute)
			tc.do(t, w, g, chief)
			w.record(t, g.ID, EventUpdate, "half done")
			lines, err := w.core.Digest(chief.Session)
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != 1 || !strings.Contains(lines[0], tc.want) || !strings.Contains(lines[0], "1 update") {
				t.Fatalf("ym digest printed %q, want %q and the update", lines, tc.want)
			}
			if again, _ := w.core.Digest(chief.Session); len(again) != 0 {
				t.Fatalf("a second ym digest printed %q again", again)
			}
			w.clock = w.clock.Add(ChiefBatch + time.Minute)
			w.loop(t, 2)
			if got := digests(w.handedTo(chief.Session)); len(got) != 0 {
				t.Fatalf("what ym digest printed woke the chief too: %q", got)
			}
		})
	}
}

func TestWhatARemoteAgentsNewsSaysWorksFromItsOrigin(t *testing.T) {
	const id = "01234567-89ab-4def-8123-456789abcdef"
	for _, tc := range []struct{ name, session, text, want string }{
		{"a hang", id, "agent 01234567 looks hung. Look at it with ym open " + id + "; end it with ym kill " + id + " --yes.",
			"agent b/01234567 looks hung. Look at it with ym open b/" + id + "; end it with ym kill b/" + id + " --yes."},
		{"an adopted agent's key holds its session", id, "agent 01234567 (adopt-" + id + ") stopped. ym revive adopt-" + id + "; ym removes /w/adopt-" + id + ".",
			"agent b/01234567 (adopt-" + id + ") stopped. ym revive b/adopt-" + id + "; ym removes /w/adopt-" + id + "."},
		{"a session's end of sentence", id, "finishes it in ym open " + id + ".", "finishes it in ym open b/" + id + "."},
		{"a process on its own host", id, "It starves every session on this host. Look at it with ym open " + id + "; end it with kill 41 if it is a leak.",
			"It starves every session on b. Look at it with ym open b/" + id + "; end it on b with kill 41 if it is a leak."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			if got := f.b.core.foreignText(tc.session, tc.text); got != tc.want {
				t.Fatalf("got  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func (w *orgWorld) record(t *testing.T, goal, kind, payload string) {
	t.Helper()
	if _, _, err := w.core.RecordGoalEvent(goal, kind, payload, ""); err != nil {
		t.Fatal(err)
	}
}
