package core

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func (w *orgWorld) steer(t *testing.T, ref, text, author string) Steer {
	t.Helper()
	st, err := w.core.SteerContext(w.board(t), ref, text, author)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// messages is every message session got, typed in as the owner or delivered in the background.
func (w *orgWorld) messages(session string) []string {
	out := w.handedTo(session)
	for _, text := range w.launches.sent {
		if rest, ok := strings.CutPrefix(text, session+": "); ok {
			out = append(out, rest)
		}
	}
	return out
}

func node(t *testing.T, b Board, id string) Node {
	t.Helper()
	n, ok := b.Node(id)
	if !ok {
		t.Fatalf("%s is not on the board", id)
	}
	return n
}

func TestASteerIsANumberedRevisionTheManagerMustAdoptExactly(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.steer(t, g.ID, "ship the importer for CSV only; TSV can wait", Owner)
	second := w.steer(t, m.ID, "ship the importer for CSV and TSV", Owner)
	if second.Revision != 2 {
		t.Fatalf("second steer is revision %d, want 2", second.Revision)
	}
	got := w.messages(m.Session)
	if len(got) != 2 || !strings.Contains(got[1], "Revision 2") || !strings.Contains(got[1], "CSV and TSV") || !strings.Contains(got[1], "ym goal adopt "+m.ID+" --revision 2") {
		t.Fatalf("manager got %q, want each revision with how to adopt it", got)
	}
	if n := node(t, w.board(t), m.ID); n.Steer == nil || n.Steer.Revision != 2 || n.Steer.Adopted() {
		t.Fatalf("board shows steer %+v, want revision 2 not adopted", n.Steer)
	}
	other, _ := w.goal(t, "rotate keys", "/repo/auth-plane")
	otherM, _ := w.core.Store.Manager(other.ManagerID)
	for _, tc := range []struct {
		name     string
		session  string
		revision int
		wantErr  string
	}{
		{"older revision", m.Session, 1, "adopt revision 2"},
		{"another manager", otherM.Session, 2, "only the one steered"},
		{"not a manager", "stranger", 2, "not a manager"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := w.core.Adopt(tc.session, m.ID, tc.revision); err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("adopt = %v, want an error saying %q", err, tc.wantErr)
			}
		})
	}
	if _, err := w.core.Adopt(m.Session, "", 2); err != nil {
		t.Fatal(err)
	}
	if n := node(t, w.board(t), m.ID); !n.Steer.Adopted() {
		t.Fatalf("revision 2 still shows unadopted after the manager adopted it")
	}
	if _, text, rev, _ := w.core.Context(g.ID); rev != 2 || text != "ship the importer for CSV and TSV" {
		t.Fatalf("context is r%d %q, want r2's plan", rev, text)
	}
}

func TestOnlyTheOwnerOrAChiefAboveMaySteer(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	_, loose := w.goal(t, "rotate keys", "/repo/auth-plane")
	lower := w.chief(t, "data", m.ID)
	upper := w.chief(t, "platform", lower.ID)
	for _, tc := range []struct {
		name, author, target string
		ok                   bool
	}{
		{"owner steers a goal", Owner, m.ID, true},
		{"owner steers a chief", Owner, upper.ID, true},
		{"chief steers its report", lower.ID, m.ID, true},
		{"chief steers a report two levels down", upper.ID, m.ID, true},
		{"chief steers a goal outside its tree", upper.ID, loose.ID, false},
		{"chief steers itself", lower.ID, lower.ID, false},
		{"manager steers its chief", m.ID, lower.ID, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := w.core.SteerContext(w.board(t), tc.target, "plan from "+tc.name, tc.author)
			if (err == nil) != tc.ok {
				t.Fatalf("steer err = %v, want ok %v", err, tc.ok)
			}
		})
	}
}

func TestAnUnadoptedSteerIsNudgedThenRaisedToWhoeverSteered(t *testing.T) {
	for _, tc := range []struct {
		name      string
		byChief   bool
		adoptAt   int
		wantNudge int
		wantOwner bool
		wantChief bool
	}{
		{name: "owner steer never adopted", wantNudge: Nudges, wantOwner: true},
		{name: "chief steer never adopted", byChief: true, wantNudge: Nudges, wantChief: true},
		{name: "adopted after the first nudge", adoptAt: 1, wantNudge: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "data", m.ID)
			author := Owner
			if tc.byChief {
				author = chief.ID
			}
			st := w.steer(t, m.ID, "CSV only", author)
			for i := range 6 {
				if tc.adoptAt > 0 && i == tc.adoptAt {
					if _, err := w.core.Adopt(m.Session, m.ID, st.Revision); err != nil {
						t.Fatal(err)
					}
				}
				w.clock = w.clock.Add(AdoptAfter + time.Minute)
				w.supervise(t)
			}
			nudges := 0
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "have not adopted revision") {
					nudges++
				}
			}
			if nudges != tc.wantNudge {
				t.Fatalf("nudged %d times, want %d", nudges, tc.wantNudge)
			}
			b := w.board(t)
			inQueue := slices.ContainsFunc(b.Needs, func(n Need) bool { return n.Steer != nil && n.Manager == m.ID })
			if inQueue != tc.wantOwner {
				t.Fatalf("owner queue holds the unadopted steer = %v, want %v", inQueue, tc.wantOwner)
			}
			told := slices.ContainsFunc(w.handedTo(chief.Session), func(s string) bool { return strings.Contains(s, "has not adopted revision") })
			if told != tc.wantChief {
				t.Fatalf("chief told = %v, want %v", told, tc.wantChief)
			}
		})
	}
}

func TestARelaunchedManagerWorksToTheNewestPlan(t *testing.T) {
	for _, adopted := range []bool{false, true} {
		w := newOrgWorld(t)
		_, m := w.goal(t, "ship the importer", "/repo/august")
		st := w.steer(t, m.ID, "CSV only, and drop the TSV agent", Owner)
		if adopted {
			w.core.Adopt(m.Session, m.ID, st.Revision)
		}
		w.clock = w.clock.Add(StartGrace + time.Minute)
		w.supervise(t)
		w.stop(m.Session)
		w.supervise(t)
		p := w.launches.starts[len(w.launches.starts)-1].Prompt
		if !strings.Contains(p, "CSV only, and drop the TSV agent") || strings.Contains(p, "It is not adopted yet") == adopted {
			t.Fatalf("adopted %v: relaunch prompt does not carry the plan as it stands:\n%s", adopted, p)
		}
	}
}

func brief(next string) Brief {
	return Brief{Assessment: "importer parses CSV", Changed: "agent A finished the parser", Uncertainty: "none", Next: next, WaitingOn: "CI on #12"}
}

func TestABriefNeedsEverySectionAndEachWriteIsANewVersion(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	for _, tc := range []struct {
		name  string
		blank func(*Brief)
	}{
		{"assessment", func(b *Brief) { b.Assessment = "" }},
		{"changed", func(b *Brief) { b.Changed = " " }},
		{"uncertainty", func(b *Brief) { b.Uncertainty = "" }},
		{"next", func(b *Brief) { b.Next = "\n" }},
		{"waiting-on", func(b *Brief) { b.WaitingOn = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := brief("merge #12")
			tc.blank(&b)
			if _, err := w.core.WriteBrief(m.Session, b); err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("brief without %s = %v, want it refused", tc.name, err)
			}
		})
	}
	if _, err := w.core.WriteBrief("stranger", brief("x")); err == nil {
		t.Fatal("a session that is no manager wrote a brief")
	}
	for i, next := range []string{"open the PR", "merge #12"} {
		b, err := w.core.WriteBrief(m.Session, brief(next))
		if err != nil || b.Version != i+1 {
			t.Fatalf("brief %d = v%d, %v", i, b.Version, err)
		}
	}
	page, _ := os.ReadFile(g.BriefPath)
	if !strings.Contains(string(page), "Brief v2") || !strings.Contains(string(page), "merge #12") || strings.Contains(string(page), "open the PR") {
		t.Fatalf("brief page is not the newest version:\n%s", page)
	}
	if all, _ := w.core.Store.Briefs(m.ID); len(all) != 2 || all[0].Next != "open the PR" {
		t.Fatalf("versions %+v, want both kept", all)
	}
}

func TestABriefGoesStaleWhenSomethingMeaningfulHappensAfterIt(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "data", m.ID)
	fresh := func(id string) Freshness { return node(t, w.board(t), id).Fresh }
	if f := fresh(m.ID); f.Brief != nil || !f.Stale() {
		t.Fatalf("a manager that never wrote a brief shows %+v, want no brief", f)
	}
	w.core.WriteBrief(chief.Session, brief("watch data"))
	w.clock = w.clock.Add(time.Minute)
	w.core.WriteBrief(m.Session, brief("open the PR"))
	if f := fresh(m.ID); f.Stale() {
		t.Fatalf("a brief just written shows stale: %+v", f)
	}
	if f := fresh(chief.ID); !f.Stale() || !strings.Contains(f.BehindWhat, m.ID) {
		t.Fatalf("chief's rollup ignores its report's newer brief: %+v", f)
	}
	w.clock = w.clock.Add(time.Minute)
	st := w.steer(t, m.ID, "CSV only", Owner)
	if f := fresh(m.ID); f.Stale() {
		t.Fatalf("an unadopted steer made the brief stale; it shows as unadopted instead: %+v", f)
	}
	w.clock = w.clock.Add(time.Minute)
	w.core.Adopt(m.Session, m.ID, st.Revision)
	if f := fresh(m.ID); !f.Stale() || f.BehindWhat != "adopted r1" {
		t.Fatalf("adopting a new plan left the brief fresh: %+v", f)
	}
}

func TestAStaleBriefNudgesItsManagerOncePerEventAndNotMidTurn(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	nudges := func() int {
		n := 0
		for _, text := range w.handedTo(m.Session) {
			if strings.Contains(text, "your brief is behind") {
				n++
			}
		}
		return n
	}
	w.set(m.Session, func(s *Session) { s.Status = Busy })
	w.clock = w.clock.Add(BriefGrace + time.Minute)
	w.supervise(t)
	if n := nudges(); n != 0 {
		t.Fatalf("nudged a busy manager mid-turn %d times", n)
	}
	w.set(m.Session, func(s *Session) { s.Status = Idle })
	for range 3 {
		w.clock = w.clock.Add(BriefGrace + time.Minute)
		w.supervise(t)
	}
	if n := nudges(); n != 1 {
		t.Fatalf("a manager with no brief was nudged %d times, want once", n)
	}
	w.core.WriteBrief(m.Session, brief("open the PR"))
	w.clock = w.clock.Add(time.Minute)
	st := w.steer(t, m.ID, "CSV only", Owner)
	w.core.Adopt(m.Session, m.ID, st.Revision)
	for range 3 {
		w.clock = w.clock.Add(BriefGrace + time.Minute)
		w.supervise(t)
	}
	if n := nudges(); n != 2 {
		t.Fatalf("after adopting a steer the stale brief drew %d nudges in all, want 2", n)
	}
	w.core.WriteBrief(m.Session, brief("ship CSV"))
	w.clock = w.clock.Add(BriefGrace + time.Minute)
	w.supervise(t)
	if n := nudges(); n != 2 {
		t.Fatalf("a refreshed brief still drew nudges: %d", n)
	}
}
