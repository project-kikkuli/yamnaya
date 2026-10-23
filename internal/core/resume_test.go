package core

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// resumes is ym's launcher with resuming: a session it launched comes back as itself, its conversation kept unless lost is set.
type resumes struct {
	*fakeLaunches
	lost    map[string]bool
	fail    map[string]error
	resumed []resumeCall
}

type resumeCall struct{ session, prompt, start string }

func (r *resumes) Bring(s Live, prompt string) error {
	if err := r.fail[s.ID]; err != nil {
		return err
	}
	r.resumed = append(r.resumed, resumeCall{s.ID, prompt, s.Cwd})
	r.w.spawn(s.ID, "")
	r.state(s.ID, "running")
	return nil
}

func (r *resumes) Resume(session, prompt, start string) (bool, error) {
	known := slices.ContainsFunc(r.launched, func(l Launched) bool { return l.Session == session }) ||
		slices.ContainsFunc(r.starts, func(n NewLaunch) bool { return n.Session == session })
	if !known {
		return false, fmt.Errorf("no launch of %s: %w", session, ErrNotFound)
	}
	if err := r.fail[session]; err != nil {
		return false, err
	}
	r.resumed = append(r.resumed, resumeCall{session, prompt, start})
	r.w.spawn(session, "")
	r.state(session, "running")
	return !r.lost[session], nil
}

func (r *resumes) state(session, st string) {
	for i := range r.launched {
		if r.launched[i].Session == session {
			r.launched[i].State = st
			return
		}
	}
	r.launched = append(r.launched, Launched{Key: "wt-" + session, Harness: "claude", Session: session, State: st})
}

func resumeWorld(t *testing.T) (*orgWorld, *resumes) {
	w := newOrgWorld(t)
	r := &resumes{fakeLaunches: w.launches, lost: map[string]bool{}, fail: map[string]error{}}
	w.core.Launches = r
	return w, r
}

// restart ends every session as a reboot of the host does: no process left, every launched pane gone.
func (w *orgWorld) restart(r *resumes) {
	w.procs = nil
	for i := range r.launched {
		if r.launched[i].Landed.IsZero() {
			r.launched[i].State = "gone"
		}
	}
}

func TestARestartedManagerOrChiefComesBackInItsOwnConversation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		chief    bool
		lost     bool
		unknown  bool
		resumed  bool
		sameID   bool
		freshRun bool
	}{
		{name: "a manager whose transcript survived", resumed: true, sameID: true},
		{name: "a chief whose transcript survived", chief: true, resumed: true, sameID: true},
		{name: "a manager whose transcript is gone starts over as itself", lost: true, resumed: true, sameID: true},
		{name: "a manager ym never launched starts fresh", unknown: true, freshRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			var g Goal
			var m Manager
			if tc.chief {
				m = w.chief(t, "platform")
			} else {
				g, m = w.goal(t, "ship the importer", "/repo/august")
			}
			w.supervise(t)
			r.state(m.Session, "running")
			if tc.unknown {
				r.launched, r.starts = nil, nil
			}
			r.lost[m.Session] = tc.lost
			if !tc.chief {
				if _, _, err := w.core.RecordGoalEvent(g.ID, EventUpdate, "CI went red on the parser", ""); err != nil {
					t.Fatal(err)
				}
			}
			w.restart(r)
			w.clock = w.clock.Add(StartGrace + time.Minute)
			w.supervise(t)
			now, _ := w.core.Store.Manager(m.ID)
			if (len(r.resumed) == 1) != tc.resumed {
				t.Fatalf("resumes %+v, want one: %v", r.resumed, tc.resumed)
			}
			if (now.Session == m.Session) != tc.sameID {
				t.Fatalf("session %s after the restart, was %s; same wanted: %v", now.Session, m.Session, tc.sameID)
			}
			if fresh := len(r.starts) > 0 && r.starts[len(r.starts)-1].Session == now.Session && now.Session != m.Session; fresh != tc.freshRun {
				t.Fatalf("fresh start %v, want %v (starts %+v)", fresh, tc.freshRun, r.starts)
			}
			if !tc.resumed {
				return
			}
			call := r.resumed[0]
			if call.session != m.Session || !strings.Contains(call.prompt, "same conversation") || !strings.Contains(call.start, m.BriefPath) {
				t.Fatalf("resume %+v lacks its session, resume note or fresh prompt", call)
			}
			if !tc.chief && !strings.Contains(call.prompt, "CI went red on the parser") {
				t.Fatalf("resumed manager was not handed what it missed:\n%s", call.prompt)
			}
			if !w.board(t).Alive(now) {
				t.Fatal("resumed manager is not alive on the board")
			}
		})
	}
}

func TestOwnerRelaunchStillStartsAManagerFresh(t *testing.T) {
	w, r := resumeWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	w.stop(m.Session)
	got, err := w.core.ReplaceManager(w.board(t), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.resumed) != 0 || got.Session == m.Session {
		t.Fatalf("ym goal relaunch resumed %+v into %s; it starts fresh", r.resumed, got.Session)
	}
}

func TestAGoalAgentWhosePaneWentIsResumedUnlessItWasStopped(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		edit    func(*orgWorld, *Launched, Goal)
		resumed bool
	}{
		{name: "pane gone under an active goal", state: "gone", resumed: true},
		{name: "pane gone, spawned by another agent of the goal", state: "gone", resumed: true, edit: func(w *orgWorld, l *Launched, g Goal) {
			w.core.Store.Link("helper", l.Parent)
			l.Parent = "helper"
		}},
		{name: "ym kill stopped it", state: "gone", edit: func(w *orgWorld, l *Launched, g Goal) { l.Killed = w.clock }},
		{name: "a kill is on record for it", state: "gone", edit: func(w *orgWorld, l *Launched, g Goal) { w.core.Store.RecordKill(l.Session, w.clock) }},
		{name: "it landed", state: "landed", edit: func(w *orgWorld, l *Launched, g Goal) { l.Landed = w.clock }},
		{name: "its agent exited", state: "exited 1"},
		{name: "tmux could not say", state: LaunchUnknown},
		{name: "no goal above it", state: "gone", edit: func(w *orgWorld, l *Launched, g Goal) { l.Parent = "someone-else" }},
		{name: "its goal is done", state: "gone", edit: func(w *orgWorld, l *Launched, g Goal) {
			g.State = GoalDone
			w.core.Store.SaveGoal(g)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			l := Launched{Key: "wt-agent", Harness: "claude", Session: "agent", State: tc.state, Parent: m.Session}
			if tc.edit != nil {
				tc.edit(w, &l, g)
			}
			r.launched = append(r.launched, l)
			if err := w.core.ReviveWork(w.board(t)); err != nil {
				t.Fatal(err)
			}
			got := slices.ContainsFunc(r.resumed, func(c resumeCall) bool { return c.session == "agent" })
			if got != tc.resumed {
				t.Fatalf("agent resumed %v, want %v", got, tc.resumed)
			}
			if got && !strings.Contains(r.resumed[0].prompt, "same conversation") {
				t.Fatalf("resumed agent was not told why: %q", r.resumed[0].prompt)
			}
		})
	}
}

func TestAGoalAgentIsResumedEachTimeItsPaneGoesButAFailedResumeOnlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail bool
		want int
	}{
		{name: "it came back and its pane went again", want: 2},
		{name: "its resume failed and it is still gone", fail: true, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			r.launched = append(r.launched, Launched{Key: "wt-agent", Harness: "claude", Session: "agent", State: "gone", Parent: m.Session})
			if tc.fail {
				r.fail["agent"] = errors.New("claude not found on PATH")
			}
			for range 2 {
				if err := w.core.ReviveWork(w.board(t)); err != nil && !tc.fail {
					t.Fatal(err)
				}
				w.clock = w.clock.Add(ResumePace)
				if err := w.core.ReviveWork(w.board(t)); err != nil && !tc.fail {
					t.Fatal(err)
				}
				w.restart(r)
				w.clock = w.clock.Add(ResumePace)
			}
			if len(r.resumed) != tc.want {
				t.Fatalf("resumed %d times, want %d: %+v", len(r.resumed), tc.want, r.resumed)
			}
		})
	}
}

func TestResumesAfterARestartArePacedOneAtATime(t *testing.T) {
	w, r := resumeWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	for i := range 3 {
		r.launched = append(r.launched, Launched{Key: fmt.Sprint("wt-", i), Session: fmt.Sprint("agent-", i), State: "gone", Parent: m.Session})
	}
	for _, step := range []struct {
		after time.Duration
		want  int
	}{{0, 1}, {ResumePace / 2, 1}, {ResumePace / 2, 2}, {ResumePace, 3}, {ResumePace, 3}} {
		w.clock = w.clock.Add(step.after)
		if err := w.core.ReviveWork(w.board(t)); err != nil {
			t.Fatal(err)
		}
		if len(r.resumed) != step.want {
			t.Fatalf("%d resumed by %s, want %d", len(r.resumed), w.clock.Format(time.TimeOnly), step.want)
		}
	}
}

func TestAColdStartRaisesOneNotificationSayingWhatCameBackAndWhatDidNot(t *testing.T) {
	w, r := resumeWorld(t)
	var shown []string
	w.core.Notify = func(title, body string) error { shown = append(shown, title+" | "+body); return nil }
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.supervise(t)
	r.state(m.Session, "running")
	r.launched = append(r.launched, Launched{Key: "wt-agent", Session: "agent", State: "running", Parent: m.Session},
		Launched{Key: "wt-broken", Session: "broken", State: "running", Parent: m.Session})
	w.spawn("agent", m.Session)
	w.clock = w.clock.Add(StartGrace + time.Minute)
	w.loop(t, 1)
	if len(shown) != 0 {
		t.Fatalf("notified %q before any restart", shown)
	}
	w.restart(r)
	r.fail["broken"] = errors.New("claude not found on PATH")
	for range 20 {
		w.clock = w.clock.Add(5 * time.Second)
		w.loop(t, 1)
	}
	if len(shown) != 1 {
		t.Fatalf("notifications %q, want exactly one after the restart", shown)
	}
	for _, want := range []string{"2 of 3", "manager " + m.ID, "agent " + Short("agent"), "Could not resume", "claude not found on PATH"} {
		if !strings.Contains(shown[0], want) {
			t.Errorf("notification %q lacks %q", shown[0], want)
		}
	}
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed := slices.ContainsFunc(events, func(e GoalEvent) bool {
		return e.Kind == EventResumeFailed && strings.Contains(e.Payload, "claude not found on PATH") && e.OwnerNews()
	})
	if !failed {
		t.Fatalf("the agent that could not resume is not news in the goal's feed: %+v", events)
	}
}

func TestAColdStartBringsBackEverySessionThatWasRunningNotJustGoalWork(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session string
		setup   func(*orgWorld, *resumes)
		back    bool
	}{
		{name: "a session ym launched, running when its server went", session: "plain", back: true, setup: func(w *orgWorld, r *resumes) {
			r.launched = append(r.launched, Launched{Key: "wt-plain", Session: "plain", State: "gone", Revive: true})
		}},
		{name: "a session ym launched that had exited before", session: "plain", setup: func(w *orgWorld, r *resumes) {
			r.launched = append(r.launched, Launched{Key: "wt-plain", Session: "plain", State: "gone"})
		}},
		{name: "a session ym launched and ym kill stopped", session: "plain", setup: func(w *orgWorld, r *resumes) {
			r.launched = append(r.launched, Launched{Key: "wt-plain", Session: "plain", State: "gone", Revive: true, Killed: w.clock})
		}},
		{name: "a session the owner started in a terminal", session: "mine", back: true, setup: func(w *orgWorld, r *resumes) {
			w.run(4000, 1, "/dev/ttys040", "claude", "mine", Idle)
		}},
		{name: "a session the owner started and ym kill stopped", session: "mine", setup: func(w *orgWorld, r *resumes) {
			w.run(4000, 1, "/dev/ttys040", "claude", "mine", Idle)
			w.core.Store.RecordKill("mine", w.clock)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			var shown []string
			w.core.Notify = func(title, body string) error { shown = append(shown, title+" | "+body); return nil }
			r.launched = append(r.launched, Launched{Key: "wt-other", Session: "other", State: "running"})
			w.spawn("other", "")
			launched := tc.session == "plain"
			if !launched {
				tc.setup(w, r)
				w.set("mine", func(s *Session) { s.Cwd = "/repo/august" })
			}
			w.loop(t, 1)
			w.restart(r)
			if launched {
				tc.setup(w, r)
			}
			for range 10 {
				w.clock = w.clock.Add(5 * time.Second)
				w.loop(t, 1)
			}
			got := slices.ContainsFunc(r.resumed, func(c resumeCall) bool { return c.session == tc.session })
			if got != tc.back {
				t.Fatalf("%s brought back %v, want %v (resumes %+v)", tc.session, got, tc.back, r.resumed)
			}
			if tc.back && (len(shown) != 1 || !strings.Contains(shown[0], Short(tc.session))) {
				t.Fatalf("notifications %q, want one naming %s", shown, tc.session)
			}
		})
	}
}

func TestOnlyAFreshListOrOneFromBeforeTheBootBringsTheOwnersSessionsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		age    time.Duration
		booted time.Duration
		back   bool
	}{
		{name: "written moments before the server went", age: 10 * time.Second, booted: -24 * time.Hour, back: true},
		{name: "written before the host rebooted overnight", age: 9 * time.Hour, booted: -1 * time.Hour, back: true},
		{name: "a serve started hours after its last look, no reboot", age: 9 * time.Hour, booted: -24 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			w.core.Boot = func() time.Time { return time.Now().Add(tc.booted) }
			r.launched = append(r.launched, Launched{Key: "wt-other", Session: "other", State: "running"})
			w.spawn("other", "")
			w.run(4000, 1, "/dev/ttys040", "claude", "mine", Idle)
			w.set("mine", func(s *Session) { s.Cwd = "/repo/august" })
			w.loop(t, 1)
			path := filepath.Join(w.core.StateDir, aliveFile)
			old := time.Now().Add(-tc.age)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			w.restart(r)
			w.loop(t, 1)
			got := slices.ContainsFunc(r.resumed, func(c resumeCall) bool { return c.session == "mine" })
			if got != tc.back {
				t.Fatalf("brought back %v, want %v", got, tc.back)
			}
		})
	}
}
