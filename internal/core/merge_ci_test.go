package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const (
	redMerge  = "a1b2c3d4e5f60718293a4b5c6d7e8f9012345678"
	redParent = "b2c3d4e5f60718293a4b5c6d7e8f901234567890"
	greenFix  = "c3d4e5f60718293a4b5c6d7e8f90123456789012"
	greenHead = "d4e5f60718293a4b5c6d7e8f9012345678901234"
	lintJob   = "checks: lint"
	unitJob   = "checks: lint / unit"
)

type jobLandings struct {
	fakeLandings
	jobs []MergeJob
	err  error
}

func (f *jobLandings) MergeJobs(string, string) ([]MergeJob, error) { return f.jobs, f.err }

func redLanding() Landing {
	return Landing{State: "MERGED", Head: "head", MergeCommit: redMerge, Passed: 159, Failed: 2}
}

func TestRedOnTheMergeCommitFailsOnlyWhatIsTheGoals(t *testing.T) {
	fixed := func(name, at string) MergeJob { return MergeJob{Name: name, Later: JobPass, LaterAt: at} }
	mains := func(name string) MergeJob {
		return MergeJob{Name: name, Later: JobFail, LaterAt: greenHead, Parent: JobFail, ParentAt: redParent}
	}
	for _, tc := range []struct {
		name    string
		landing Landing
		jobs    []MergeJob
		err     error
		// fact is the fact's verdict word; launched is whether a verifier then runs.
		fact     string
		launched bool
		have     []string
		lack     []string
	}{
		{name: "red on merge and parent, green later", fact: "ok", launched: true,
			jobs: []MergeJob{
				{Name: lintJob, Later: JobPass, LaterAt: greenHead},
				{Name: unitJob, Later: JobPass, LaterAt: greenFix},
			},
			have: []string{"2 of 161 checks failed on a1b2c3d4e5", "since green on main", lintJob + " (d4e5f60718)", unitJob + " (c3d4e5f607)"}},
		{name: "new on the merge, green later", fact: "ok", launched: true, jobs: []MergeJob{
			{Name: lintJob, Later: JobPass, LaterAt: greenHead, Parent: JobPass, ParentAt: redParent}},
			have: []string{"since green on main"}},
		{name: "red on the parent and still red is main's", fact: "ok", launched: true,
			jobs: []MergeJob{mains(lintJob), mains(unitJob)},
			have: []string{"main's", lintJob + " (b2c3d4e5f6)", unitJob + " (b2c3d4e5f6)"}},
		{name: "new with the merge and still red fails", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, Later: JobFail, LaterAt: greenHead, Parent: JobPass, ParentAt: redParent}},
			have: []string{"not red before this merge", lintJob + " (still red on d4e5f60718)"}, lack: []string{"main's"}},
		{name: "new job the parent never ran, still red, fails", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, Later: JobFail, LaterAt: greenHead, ParentAt: redParent}},
			have: []string{"not red before this merge"}},
		{name: "no later commit, green parent: fails as it always did", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, Parent: JobPass, ParentAt: redParent}},
			have: []string{"2 of 161 checks failed", lintJob + " (no later result in the commits read)"}},
		{name: "skipped or neutral later and on the parent read as nothing: fails", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, ParentAt: redParent}},
			have: []string{lintJob + " (no later result in the commits read)"}, lack: []string{"main's", "since green"}},
		{name: "no later commit, red parent: fails, not main's", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, Parent: JobFail, ParentAt: redParent}},
			have: []string{"2 of 161 checks failed", lintJob + " (also red on the parent b2c3d4e5f6, no later result in the commits read)"},
			lack: []string{"main's", "not this goal's"}},
		{name: "red parent, only skipped, neutral, cancelled or absent runs later: fails, not main's", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, Parent: JobFail, ParentAt: redParent}, {Name: unitJob, Parent: JobFail, ParentAt: redParent}},
			have: []string{lintJob + " (also red on the parent", unitJob + " (also red on the parent"},
			lack: []string{"main's", "not this goal's"}},
		{name: "no later result, parent's run in progress: fails", fact: "fail",
			jobs: []MergeJob{{Name: lintJob, Parent: JobPending, ParentAt: redParent}},
			have: []string{lintJob + " (no later result in the commits read)"}, lack: []string{"main's"}},
		{name: "red parent, later run in progress: rechecks, not main's yet", fact: "pending",
			jobs: []MergeJob{{Name: lintJob, Later: JobPending, LaterAt: greenHead, Parent: JobFail, ParentAt: redParent}},
			have: []string{lintJob + " (still running on d4e5f60718)"}, lack: []string{"main's", "not this goal's"}},
		{name: "red parent, finished red later: main's", fact: "ok", launched: true,
			jobs: []MergeJob{{Name: lintJob, Later: JobFail, LaterAt: greenHead, Parent: JobFail, ParentAt: redParent}},
			have: []string{"main's", lintJob + " (b2c3d4e5f6)"}},
		{name: "new with the merge, later run in progress: rechecks", fact: "pending",
			jobs: []MergeJob{{Name: lintJob, Later: JobPending, LaterAt: greenHead, Parent: JobPass, ParentAt: redParent}},
			have: []string{lintJob + " (still running on d4e5f60718)"}},
		{name: "parent's run in progress: rechecks", fact: "pending",
			jobs: []MergeJob{{Name: lintJob, Later: JobFail, LaterAt: greenHead, Parent: JobPending, ParentAt: redParent}},
			have: []string{"still running on the parent b2c3d4e5f6"}},
		{name: "one job main's, one the goal's: fails on the goal's alone", fact: "fail",
			jobs: []MergeJob{fixed(unitJob, greenHead), {Name: lintJob, Later: JobFail, LaterAt: greenHead, Parent: JobPass}},
			have: []string{"not red before this merge: " + lintJob, "since green on main: " + unitJob}},
		{name: "main's red with other checks running on the merge: rechecks", fact: "pending",
			landing: Landing{State: "MERGED", Head: "head", MergeCommit: redMerge, Passed: 150, Failed: 2, Pending: 9},
			jobs:    []MergeJob{mains(lintJob)}, have: []string{"main's", "9 of 161 checks still running"}},
		{name: "jobs unread: rechecks", fact: "pending", err: errors.New("GitHub timed out"), have: []string{"GitHub timed out"}},
		{name: "no job explains the red: fails", fact: "fail", have: []string{"2 of 161 checks failed on a1b2c3d4e5"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			landing := tc.landing
			if landing.State == "" {
				landing = redLanding()
			}
			w.landings[pr1] = landing
			w.core.Landings = &jobLandings{fakeLandings: w.landings, jobs: tc.jobs, err: tc.err}
			w.done(t, "merged", pr1)
			w.tick(t, time.Second)
			g, v := w.state(t)
			var ci Fact
			for _, f := range v.Facts {
				if f.Check == ciFact {
					ci = f
				}
			}
			line := ci.String()
			if !strings.HasPrefix(line, tc.fact+" "+ciFact) {
				t.Fatalf("fact %q, want %s", line, tc.fact)
			}
			for _, want := range tc.have {
				if !strings.Contains(line, want) {
					t.Fatalf("fact %q lacks %q", line, want)
				}
			}
			for _, not := range tc.lack {
				if strings.Contains(line, not) {
					t.Fatalf("fact %q says %q", line, not)
				}
			}
			if got := len(w.verifiers()) == 1; got != tc.launched {
				t.Fatalf("verifier launched %v, want %v; fact %q", got, tc.launched, line)
			}
			wantState := map[string]string{"ok": GoalVerifying, "pending": GoalVerifying, "fail": GoalActive}[tc.fact]
			if g.State != wantState {
				t.Fatalf("goal %s, want %s", g.State, wantState)
			}
			if tc.launched {
				prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
				if !strings.Contains(prompt, line) || !strings.Contains(prompt, "Neither is a reason to fail") {
					t.Fatalf("verifier prompt lacks the attribution:\n%s", prompt)
				}
			}
		})
	}
}

func TestMainMovingWhileAVerifierRunsKeepsItsPassUnlessTheGoalIsToBlame(t *testing.T) {
	green := MergeJob{Name: lintJob, Later: JobPass, LaterAt: greenFix}
	for _, tc := range []struct {
		name string
		then MergeJob
		pass bool
	}{
		{"green on a newer commit", MergeJob{Name: lintJob, Later: JobPass, LaterAt: greenHead}, true},
		{"main's job goes from named on the parent to green", MergeJob{Name: lintJob, Later: JobPass, LaterAt: greenHead, Parent: JobFail}, true},
		{"the job now reads as the goal's", MergeJob{Name: lintJob, Later: JobFail, LaterAt: greenHead, Parent: JobPass}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.landings[pr1] = redLanding()
			jobs := &jobLandings{fakeLandings: w.landings, jobs: []MergeJob{green}}
			w.core.Landings = jobs
			w.done(t, "merged", pr1)
			w.tick(t, time.Second)
			g, v := w.state(t)
			jobs.jobs = []MergeJob{tc.then}
			_, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil)
			if (err == nil) != tc.pass {
				t.Fatalf("pass recorded %v, want %v: %v", err == nil, tc.pass, err)
			}
			g, _ = w.state(t)
			if want := map[bool]string{true: GoalDone, false: GoalActive}[tc.pass]; g.State != want {
				t.Fatalf("goal %s, want %s", g.State, want)
			}
		})
	}
}

type convergingLandings struct {
	*jobLandings
	converge func()
}

func (f convergingLandings) Landing(url string) (Landing, error) {
	f.converge()
	return f.jobLandings.Landing(url)
}

func TestACachedRowGoingGreenDuringThePassKeepsAPassOverRedThatIsNotTheGoals(t *testing.T) {
	for _, tc := range []struct {
		name string
		job  MergeJob
	}{
		{"main's", MergeJob{Name: lintJob, Later: JobFail, LaterAt: greenHead, Parent: JobFail, ParentAt: redParent}},
		{"since green", MergeJob{Name: lintJob, Later: JobPass, LaterAt: greenHead}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.landings[pr1] = redLanding()
			if err := w.core.Store.SavePRStatus(pr1, PRStatus{State: "MERGED", Merge: PRMergeFacts{OID: "head"}, Failed: 2, Fetched: w.clock}); err != nil {
				t.Fatal(err)
			}
			jobs := &jobLandings{fakeLandings: w.landings, jobs: []MergeJob{tc.job}}
			w.core.Landings = jobs
			w.done(t, "merged", pr1)
			w.tick(t, time.Second)
			g, v := w.state(t)
			w.core.Landings = convergingLandings{jobs, func() {
				if err := w.core.Store.SavePRStatus(pr1, PRStatus{State: "MERGED", Merge: PRMergeFacts{OID: "head"}, Passed: 3, Fetched: w.clock}); err != nil {
					t.Fatal(err)
				}
			}}
			if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			if g, _ = w.state(t); g.State != GoalDone {
				t.Fatalf("goal %s, want done", g.State)
			}
		})
	}
}

func TestANewAttemptReadsMergeCIAfresh(t *testing.T) {
	w := newVerifyWorld(t)
	w.landings[pr1] = redLanding()
	jobs := &jobLandings{fakeLandings: w.landings, jobs: []MergeJob{{Name: lintJob, Later: JobFail, LaterAt: redParent, Parent: JobPass}}}
	w.core.Landings = jobs
	w.done(t, "merged", pr1)
	w.tick(t, time.Second)
	if g, _ := w.state(t); g.State != GoalActive {
		t.Fatalf("goal %s after a red fact, want active", g.State)
	}
	jobs.jobs = []MergeJob{{Name: lintJob, Later: JobPass, LaterAt: greenFix, Parent: JobPass}}
	w.done(t, "merged, and main is green again", pr1)
	w.tick(t, time.Second)
	if g, v := w.state(t); g.State != GoalVerifying || len(w.verifiers()) != 1 {
		t.Fatalf("goal %s with facts %+v after main went green, want a verifier", g.State, v.Facts)
	}
}
