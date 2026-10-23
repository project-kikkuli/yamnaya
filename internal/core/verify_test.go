package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type fakeLandings map[string]Landing

func (f fakeLandings) Landing(url string) (Landing, error) {
	l, ok := f[url]
	if !ok {
		return l, errors.New("GitHub timed out")
	}
	return l, nil
}

type verifyWorld struct {
	*orgWorld
	landings fakeLandings
	goal     Goal
	manager  Manager
}

func newVerifyWorld(t *testing.T) *verifyWorld {
	t.Helper()
	w := &verifyWorld{orgWorld: newOrgWorld(t), landings: fakeLandings{}}
	w.core.Landings = w.landings
	w.goal, w.manager = w.orgWorld.goal(t, "make the importer handle CSV", "/repo/august")
	w.set(w.manager.Session, func(s *Session) { s.Status = Busy })
	return w
}

func (w *verifyWorld) done(t *testing.T, summary string, evidence ...string) Verification {
	t.Helper()
	_, v, err := w.core.FinishGoal(w.manager.Session, summary, evidence)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func (w *verifyWorld) state(t *testing.T) (Goal, Verification) {
	t.Helper()
	g, err := w.core.Store.Goal(w.goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	all, err := w.core.Store.Verifications(g.ID)
	if err != nil || len(all) == 0 {
		t.Fatalf("verifications of %s: %v, %v", g.ID, all, err)
	}
	return g, all[len(all)-1]
}

// verifiers are the sessions ym launched to verify, oldest first.
func (w *verifyWorld) verifiers() []string {
	var out []string
	for _, s := range w.launches.starts {
		if strings.HasPrefix(s.Slug, "verify-") {
			out = append(out, s.Session)
		}
	}
	return out
}

func (w *verifyWorld) toManager() string { return strings.Join(w.handedTo(w.manager.Session), "\n") }

func (w *verifyWorld) tick(t *testing.T, d time.Duration) Board {
	t.Helper()
	w.clock = w.clock.Add(d)
	if err := w.core.Loop(w.board(t)); err != nil {
		t.Fatal(err)
	}
	return w.board(t)
}

const pr1 = "https://github.com/acme/august/pull/7"

func TestVerificationDecidesTheGoalFromFactsAndAFreshVerifier(t *testing.T) {
	merged := Landing{State: "MERGED", MergeCommit: "abcdef1234567", Passed: 3}
	for _, tc := range []struct {
		name     string
		landing  *Landing
		verdict  string
		findings []string
		// launched is whether facts passed and a verifier ran.
		launched     bool
		state        string
		ownerRow     string
		toManager    string
		findingsHave string
	}{
		{name: "no pull requests, verifier passes", verdict: VerdictPass, launched: true, state: GoalDone, ownerRow: "done", toManager: "passed verification"},
		{name: "merged green, verifier passes", landing: &merged, verdict: VerdictPass, launched: true, state: GoalDone, ownerRow: "done", toManager: "passed verification"},
		{name: "merged green, verifier fails", landing: &merged, verdict: VerdictFail, findings: []string{"CSV with quoted commas is rejected: importer/csv.go:40"},
			launched: true, state: GoalActive, toManager: "quoted commas", findingsHave: "quoted commas"},
		{name: "pull request still open", landing: &Landing{State: "OPEN"}, state: GoalActive, toManager: "still open", findingsHave: "fail merged " + pr1 + ": still open"},
		{name: "closed unmerged", landing: &Landing{State: "CLOSED"}, state: GoalActive, findingsHave: "closed without merging"},
		{name: "red CI on merge commit", landing: &Landing{State: "MERGED", MergeCommit: "abcdef1234567", Passed: 2, Failed: 1}, state: GoalActive,
			findingsHave: "1 of 3 checks failed on abcdef1234"},
		{name: "unresolved review thread", landing: &Landing{State: "MERGED", MergeCommit: "abc", Passed: 1, Unresolved: 2}, state: GoalActive,
			findingsHave: "fail review threads " + pr1 + ": 2 unresolved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			var evidence []string
			if tc.landing != nil {
				w.landings[pr1] = *tc.landing
				evidence = append(evidence, pr1)
			}
			w.done(t, "importer reads CSV", evidence...)
			if g, _ := w.state(t); g.State != GoalVerifying || len(goalRows(w.board(t))) != 0 {
				t.Fatalf("after ym goal done the goal is %s with owner rows %q, want verifying and nothing for the owner", g.State, goalRows(w.board(t)))
			}
			w.tick(t, time.Second)
			if got := len(w.verifiers()) == 1; got != tc.launched {
				t.Fatalf("verifier launched %v, want %v", got, tc.launched)
			}
			if tc.launched {
				verifier := w.verifiers()[0]
				if verifier == w.manager.Session {
					t.Fatal("the verifier is the manager's own session")
				}
				prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
				for _, want := range []string{w.goal.Ask, "importer reads CSV", "ym goal verdict " + w.goal.ID} {
					if !strings.Contains(prompt, want) {
						t.Fatalf("verifier prompt lacks %q:\n%s", want, prompt)
					}
				}
				if _, err := w.core.RecordVerdict(w.manager.Session, w.goal.ID, VerdictPass, nil); err == nil {
					t.Fatal("the manager recorded a verdict on its own goal")
				}
				if _, err := w.core.RecordVerdict(verifier, w.goal.ID, tc.verdict, tc.findings); err != nil {
					t.Fatal(err)
				}
				w.set(verifier, func(s *Session) { s.Status = Busy })
				w.tick(t, time.Second)
				if !slices.Contains(w.launches.forgot, verifier) {
					t.Fatalf("decided verifier %s still runs; forgot %q", verifier, w.launches.forgot)
				}
			}
			g, v := w.state(t)
			if g.State != tc.state {
				t.Fatalf("goal %s, want %s; verification %+v", g.State, tc.state, v)
			}
			var want []string
			if tc.ownerRow != "" {
				want = []string{g.ID + " " + tc.ownerRow}
			}
			if rows := goalRows(w.board(t)); !slices.Equal(rows, want) {
				t.Fatalf("owner rows %q, want %q", rows, want)
			}
			if !strings.Contains(w.toManager(), tc.toManager) {
				t.Fatalf("manager was told %q, want it to mention %q", w.toManager(), tc.toManager)
			}
			if !strings.Contains(strings.Join(v.Findings, "\n"), tc.findingsHave) {
				t.Fatalf("findings %q, want %q", v.Findings, tc.findingsHave)
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []string
			for _, e := range events {
				kinds = append(kinds, e.Kind)
				if e.Delivered.IsZero() {
					t.Fatalf("event %s %q never reached the manager", e.Kind, e.Payload)
				}
			}
			want = []string{EventVerifying, EventFailedVerify}
			if tc.state == GoalDone {
				want = []string{EventVerifying, EventVerified, EventDone}
			}
			if !slices.Equal(kinds, want) {
				t.Fatalf("goal events %q, want %q", kinds, want)
			}
			if tc.state == GoalActive && !strings.Contains(w.toManager(), "- "+EventFailedVerify+": attempt 1 failed") {
				t.Fatalf("findings did not come in the manager's wake: %q", w.toManager())
			}
		})
	}
}

func TestAFailedVerificationReopensTheGoalAndTheNextDoneIsANewAttempt(t *testing.T) {
	w := newVerifyWorld(t)
	w.done(t, "importer reads CSV")
	w.tick(t, time.Second)
	if _, err := w.core.RecordVerdict(w.verifiers()[0], w.goal.ID, VerdictFail, nil); err == nil {
		t.Fatal("a failing verdict without findings was accepted")
	}
	if _, err := w.core.RecordVerdict(w.verifiers()[0], w.goal.ID, VerdictFail, []string{"no tests for headers"}); err != nil {
		t.Fatal(err)
	}
	v := w.done(t, "importer reads CSV, headers tested")
	if v.Attempt != 2 {
		t.Fatalf("second done is attempt %d, want 2", v.Attempt)
	}
	w.tick(t, time.Second)
	if vs := w.verifiers(); len(vs) != 2 || vs[0] == vs[1] {
		t.Fatalf("verifiers %q, want a fresh second one", vs)
	}
	if _, err := w.core.RecordVerdict(w.verifiers()[1], w.goal.ID, VerdictPass, []string{"headers tested in csv_test.go"}); err != nil {
		t.Fatal(err)
	}
	if g, _ := w.state(t); g.State != GoalDone {
		t.Fatalf("goal %s after passing attempt 2", g.State)
	}
}

func TestPendingFactsWaitThenLaunchOrGiveUp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		settle  *Landing
		wait    time.Duration
		state   string
		launchd bool
	}{
		{name: "CI finishes green", settle: &Landing{State: "MERGED", MergeCommit: "abc", Passed: 4}, wait: 2 * time.Minute, state: GoalVerifying, launchd: true},
		{name: "GitHub never answers", wait: FactsTimeout, state: GoalStuck},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.landings[pr1] = Landing{State: "MERGED", MergeCommit: "abc", Passed: 1, Pending: 2}
			w.done(t, "shipped "+pr1)
			w.tick(t, time.Second)
			if g, _ := w.state(t); g.State != GoalVerifying || len(w.verifiers()) != 0 {
				t.Fatalf("with CI running the goal is %s and %d verifiers ran, want verifying and none", g.State, len(w.verifiers()))
			}
			delete(w.landings, pr1)
			if tc.settle != nil {
				w.landings[pr1] = *tc.settle
			}
			w.tick(t, tc.wait)
			g, _ := w.state(t)
			if g.State != tc.state || (len(w.verifiers()) == 1) != tc.launchd {
				t.Fatalf("goal %s with %d verifiers, want %s launched %v", g.State, len(w.verifiers()), tc.state, tc.launchd)
			}
			if tc.state == GoalStuck && !slices.Equal(goalRows(w.board(t)), []string{g.ID + " stuck"}) {
				t.Fatalf("owner rows %q, want the stuck goal", goalRows(w.board(t)))
			}
		})
	}
}

func TestAVerifierThatDiesOrHangsIsReplacedThenTheGoalShowsStuck(t *testing.T) {
	for _, tc := range []struct {
		name string
		fail func(w *verifyWorld, session string)
		wait time.Duration
	}{
		{name: "crash", fail: func(w *verifyWorld, s string) { w.stop(s) }, wait: StartGrace},
		{name: "hang", fail: func(w *verifyWorld, s string) { w.set(s, func(x *Session) { x.Status = Busy }) }, wait: VerifyTimeout},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.done(t, "importer reads CSV")
			w.tick(t, time.Second)
			for i := range VerifierLaunches {
				vs := w.verifiers()
				if len(vs) != i+1 {
					t.Fatalf("after %d failures %d verifiers ran, want %d", i, len(vs), i+1)
				}
				tc.fail(w, vs[i])
				w.tick(t, tc.wait+backoff(i+1))
			}
			g, v := w.state(t)
			if g.State != GoalStuck || v.Verdict != VerdictError || len(w.verifiers()) != VerifierLaunches {
				t.Fatalf("goal %s verdict %q with %d verifiers, want stuck after %d", g.State, v.Verdict, len(w.verifiers()), VerifierLaunches)
			}
			if !slices.Equal(goalRows(w.board(t)), []string{g.ID + " stuck"}) || !strings.Contains(w.toManager(), "could not finish") {
				t.Fatalf("owner rows %q, manager told %q", goalRows(w.board(t)), w.toManager())
			}
			w.tick(t, time.Hour)
			if g, _ := w.state(t); g.State != GoalStuck {
				t.Fatalf("a stuck verification became %s on its own", g.State)
			}
		})
	}
}

func TestAnIdleVerifierIsRemindedOnce(t *testing.T) {
	w := newVerifyWorld(t)
	w.done(t, "importer reads CSV")
	w.tick(t, time.Second)
	verifier := w.verifiers()[0]
	w.tick(t, StartGrace)
	w.tick(t, time.Minute)
	if got := w.handedTo(verifier); len(got) != 1 || !strings.Contains(got[0], "ym goal verdict "+w.goal.ID) {
		t.Fatalf("idle verifier was told %q, want one reminder", got)
	}
}

func TestTheGoalsAgentPullRequestsAreChecked(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(map[bool]string{false: "agent running", true: "agent exited"}[exited], func(t *testing.T) { agentPRChecked(t, exited) })
	}
}

func agentPRChecked(t *testing.T, exited bool) {
	w := newVerifyWorld(t)
	w.spawn("agent", w.manager.Session)
	w.set("agent", func(s *Session) { s.Status = Busy; s.PRs = []PR{{URL: pr1, Repo: "acme/august", Number: 7}} })
	w.landings[pr1] = Landing{State: "OPEN"}
	if err := w.core.Store.SavePRStatus(pr1, PRStatus{State: "OPEN", Fetched: w.clock}); err != nil {
		t.Fatal(err)
	}
	if exited {
		w.tick(t, time.Second)
		w.stop("agent")
	}
	w.done(t, "importer reads CSV")
	w.tick(t, time.Second)
	g, v := w.state(t)
	if g.State != GoalActive || !strings.Contains(strings.Join(v.Findings, "\n"), pr1+": still open") {
		t.Fatalf("an agent's open pull request left the goal %s with findings %q", g.State, v.Findings)
	}
}
