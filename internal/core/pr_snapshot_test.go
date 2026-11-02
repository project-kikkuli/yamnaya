package core

import (
	"testing"
	"time"
)

type landingCall func(string) (Landing, error)

func (f landingCall) Landing(url string) (Landing, error) { return f(url) }

func TestFreshVerificationSurvivesCacheConvergenceAndIrrelevantMetadata(t *testing.T) {
	for _, when := range []string{"before launch", "after launch", "during pass"} {
		t.Run(when, func(t *testing.T) {
			w := newVerifyWorld(t)
			fresh := Landing{State: "MERGED", Head: "actual", MergeCommit: "merge", Passed: 2}
			w.landings[pr1] = fresh
			stale := PRStatus{State: "OPEN", Pending: 1, Merge: PRMergeFacts{OID: "actual", Error: "old lookup error"}, Fetched: w.clock}
			if err := w.core.Store.SavePRStatus(pr1, stale); err != nil {
				t.Fatal(err)
			}
			w.done(t, "complete", pr1)
			converge := func() {
				t.Helper()
				if err := w.core.Store.SavePRStatus(pr1, PRStatus{State: "MERGED", Merge: PRMergeFacts{OID: "actual", Error: "unrelated readiness error", Mergeable: "UNKNOWN"}, Fetched: w.clock}); err != nil {
					t.Fatal(err)
				}
			}
			if when == "before launch" {
				converge()
			}
			w.tick(t, time.Second)
			g, v := w.state(t)
			if when == "after launch" {
				converge()
			}
			if when == "during pass" {
				w.core.Landings = landingCall(func(string) (Landing, error) { converge(); return fresh, nil })
			}
			if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			g, _ = w.state(t)
			if g.State != GoalDone {
				t.Fatalf("cache convergence invalidated checked work: %+v", g)
			}
		})
	}
}

func TestContradictoryCacheChangeDuringFreshPassCannotCommit(t *testing.T) {
	for _, change := range []string{"head", "red CI", "pending CI", "threads", "state"} {
		t.Run(change, func(t *testing.T) {
			w := newVerifyWorld(t)
			fresh := Landing{State: "MERGED", Head: "checked-head", MergeCommit: "merge", Passed: 1}
			w.landings[pr1] = fresh
			w.done(t, "complete", pr1)
			w.tick(t, time.Second)
			g, v := w.state(t)
			w.core.Landings = landingCall(func(string) (Landing, error) {
				status := PRStatus{State: "MERGED", Merge: PRMergeFacts{OID: "checked-head"}, Fetched: w.clock}
				switch change {
				case "head":
					status.Merge.OID = "new-head"
				case "red CI":
					status.Failed = 1
				case "pending CI":
					status.Pending = 1
				case "threads":
					status.Unresolved = 1
				case "state":
					status.State = "OPEN"
				}
				if err := w.core.Store.SavePRStatus(pr1, status); err != nil {
					t.Fatal(err)
				}
				return fresh, nil
			})
			if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err == nil {
				t.Fatal("accepted contradictory current facts")
			}
			g, v = w.state(t)
			if g.State != GoalActive || v.Verdict != VerdictSuperseded {
				t.Fatalf("stale claim retained: %+v %+v", g, v)
			}
		})
	}
}
