package cli

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

type handoffPRSource struct{ status core.PRStatus }

func (s *handoffPRSource) PRStatus(string) (core.PRStatus, error) { return s.status, nil }

func handoffWorld(t *testing.T) (*core.Core, work.Scope, *handoffPRSource) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", "git@github.com:example/query.git"}} {
		if raw, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", raw, err)
		}
	}
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	w, _ := s.Work.Ensure("fixture", dir, "Query changes")
	w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "bind", Target: "lead", Execution: &work.Execution{Harness: "claude", Directory: dir}})
	if err != nil {
		t.Fatal(err)
	}
	src := &handoffPRSource{status: core.PRStatus{State: "OPEN", Title: "Caller migration", Passed: 12, Pending: 1, Merge: core.PRMergeFacts{Author: "mira", Viewer: "mira", OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}}
	c := &core.Core{Store: s, PRs: src}
	c.CheckReview = func(s work.Source) (work.Review, error) {
		return work.Review{ID: s.URL, URL: s.URL, Kind: "PR", Title: "Caller migration", Version: src.status.Merge.OID + ":body", Order: int64(src.status.Commits), Requester: s.Requester, Question: s.Question}, nil
	}
	return c, w, src
}

func reviewClaim(w work.Scope, event string) handoff.Claim {
	return handoff.Claim{ID: event, Event: handoff.Event{ID: event, Work: w.ID, Session: "lead", Agent: "shepherd", Harness: "claude", Path: "/tmp/agent.jsonl", Text: "#42 is green and ready for your review."}, Kind: "review", Target: "#42", Topic: "review-42", Question: "Review #42?", Quote: "#42 is green and ready for your review."}
}

func TestCapturedReviewRemainsIndependentOfMergeClaimsAndOwnerReceipts(t *testing.T) {
	c, w, src := handoffWorld(t)
	claim := reviewClaim(w, "first")
	target, err := applyHandoff(c, claim)
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/example/query/pull/42"
	w, _ = c.Store.Work.Get(w.ID)
	r := w.Reviews[url]
	if target != "review:"+url || !r.Pending() || r.Provisional || r.Handoff == nil || r.Handoff.Event != "first" || r.Handoff.Version != "head1:body" {
		t.Fatalf("handoff lost: %+v", r)
	}
	if err = captureMergeReadiness(c); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	if w.Reviews[work.MergeReviewID(url)].Pending() {
		t.Fatal("agent claim overrode independently observed unfinished CI")
	}
	w, err = c.ChangeWork(w, work.Event{Type: "reviewed", Actor: "owner", Target: url})
	if err != nil {
		t.Fatal(err)
	}
	src.status.Pending = 0
	c.Store.SavePRStatus(url, src.status)
	if err = captureMergeReadiness(c); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	mergeID := work.MergeReviewID(url)
	w, err = c.ChangeWork(w, work.Event{Type: "dismiss-merge", Actor: "owner", Target: mergeID})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"parent-summary", "subagent-summary"} {
		if _, err = applyHandoff(c, reviewClaim(w, event)); err != nil {
			t.Fatal(err)
		}
	}
	w, _ = c.Store.Work.Get(w.ID)
	if len(w.Reviews) != 2 || w.Reviews[url].State != work.Reviewed || !w.Reviews[mergeID].Dismissed {
		t.Fatal("repeated handoff reopened owner receipts or duplicated merge/review tasks")
	}
	src.status.Merge.OID = "head2"
	src.status.Commits = 2
	if _, err = applyHandoff(c, reviewClaim(w, "revised-head")); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	if !w.Reviews[url].Pending() || w.Reviews[url].ReviewedVersion != "head1:body" || w.Reviews[url].Version != "head2:body" {
		t.Fatalf("new head did not retain and supersede old review receipt: %+v", w.Reviews[url])
	}
	w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "close"})
	if err != nil {
		t.Fatal(err)
	}
	if !w.Reviews[url].Pending() {
		t.Fatal("session archival lost outstanding request")
	}
}

func TestDiscoveredColleaguePRDoesNotBecomeAPersonalHandoff(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			c, w, src := handoffWorld(t)
			src.status.Merge.Author = "sol"
			if registered {
				var err error
				w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Source: work.Source{Kind: "github", Origin: "session-pr", URL: "https://github.com/example/query/pull/42"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := applyHandoff(c, reviewClaim(w, "colleague")); err == nil {
				t.Fatal("colleague PR became a personal review obligation")
			}
			w, _ = c.Store.Work.Get(w.ID)
			if len(w.Reviews) != 0 {
				t.Fatal("unverified ownership created a review")
			}
		})
	}
}

func TestAnsweredAuthorizationIsReconciledWithoutASecondOwnerQuestion(t *testing.T) {
	c, w, _ := handoffWorld(t)
	it, err := c.Store.AddItem(core.Item{Scope: w.Repo, Session: "lead", Harness: "claude", Kind: core.KindGo, Question: "Start A+B?", Rec: "Go A+B"})
	if err != nil {
		t.Fatal(err)
	}
	it, err = c.Store.Transition(it.Key(), core.ItemAnswered, "Go A+B now")
	if err != nil {
		t.Fatal(err)
	}
	claim := handoff.Claim{ID: "go", Event: handoff.Event{ID: "request", Work: w.ID, Session: "lead", Agent: "lead", Harness: "claude", Text: "Reply go A+B to start."}, Kind: "authorization", Topic: "start-a-b", Question: "Start A+B?", Quote: "Reply go A+B to start.", Existing: "item:" + it.Key()}
	for _, id := range []string{"first", "repeat"} {
		claim.ID = id
		if _, err = applyHandoff(c, claim); err != nil {
			t.Fatal(err)
		}
	}
	items, _ := c.Store.Items(core.ItemFilter{Scopes: []string{w.Repo}})
	if len(items) != 1 || items[0].Answer != "Go A+B now" {
		t.Fatal("owner answer was duplicated or changed")
	}
	history, _ := c.Store.Work.History(w.ID, 0)
	messages := 0
	for _, e := range history {
		if e.Type == "message" {
			messages++
			if !strings.Contains(e.Text, "Go A+B now") || !strings.Contains(e.Text, "Recorded owner response") || e.Actor != "Yamnaya ledger" {
				t.Fatalf("owner provenance missing: %+v", e)
			}
		}
	}
	if messages != 1 {
		t.Fatalf("answer deliveries %d", messages)
	}
}

func TestUnregisteredDecisionIsDurableAndIdempotent(t *testing.T) {
	c, w, _ := handoffWorld(t)
	claim := handoff.Claim{ID: "choice", Event: handoff.Event{ID: "choice", Work: w.ID, Session: "lead", Agent: "lead", Harness: "codex", Path: "/tmp/rollout.jsonl", Text: "Which cache layout should we use?"}, Kind: "decision", Topic: "cache layout", Question: "Choose the cache layout", Quote: "Which cache layout should we use?"}
	var first string
	for i := range 2 {
		claim.ID = fmt.Sprint(i)
		target, err := applyHandoff(c, claim)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = target
		} else if first != target {
			t.Fatal("same decision duplicated")
		}
	}
	items, _ := c.Store.Items(core.ItemFilter{Scopes: []string{w.Repo}})
	if len(items) != 1 || items[0].State != core.ItemOpen || items[0].Answer != "" || !strings.Contains(items[0].Assumption, claim.Event.Path) {
		t.Fatalf("decision/provenance missing: %+v", items)
	}
}

func TestChatActionIsCapturedWithItsOriginAndNoGrantedApproval(t *testing.T) {
	c, w, _ := handoffWorld(t)
	claim := handoff.Claim{ID: "chat", Event: handoff.Event{ID: "native-message", Work: w.ID, Session: "lead", Agent: "helper", Harness: "claude", Path: "/tmp/agent.jsonl", Text: "Switch to this chat and enter the login code."}, Kind: "chat", Topic: "remote login code", Question: "Enter the login code in this chat", Quote: "Switch to this chat and enter the login code."}
	target, err := applyHandoff(c, claim)
	if err != nil {
		t.Fatal(err)
	}
	it, err := c.Store.Item(strings.TrimPrefix(target, "item:"))
	if err != nil {
		t.Fatal(err)
	}
	if it.Action != "chat" || it.Kind != core.KindChore || it.Session != "lead" || it.Harness != "claude" || it.Answer != "" || it.State != core.ItemOpen {
		t.Fatalf("native chat action lost: %+v", it)
	}
	claim.ID = "parent-recap"
	again, err := applyHandoff(c, claim)
	if err != nil || again != target {
		t.Fatal("repeated chat request duplicated the task")
	}
}

func TestRequestedArtifactMatchesTheExistingReview(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		ok           bool
	}{
		{"registered target", "", true},
		{"bare number", "#42", true},
		{"qualified number", "example/query#42", true},
		{"canonical URL", "https://github.com/example/query/pull/42", true},
		{"different PR", "#43", false},
		{"ungrounded number", "#44", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, w, _ := handoffWorld(t)
			first := reviewClaim(w, "first")
			id, err := applyHandoff(c, first)
			if err != nil {
				t.Fatal(err)
			}
			next := reviewClaim(w, "repeat")
			next.Event.Text += " Compare example/query#42 with #43."
			next.Existing, next.Target = id, tc.target
			_, err = applyHandoff(c, next)
			if (err == nil) != tc.ok {
				t.Fatalf("match accepted=%t: %v", err == nil, err)
			}
		})
	}
}

func TestOpaqueArtifactRecapDoesNotInventARevision(t *testing.T) {
	c, w, _ := handoffWorld(t)
	url := "https://artifacts.example/design"
	claim := handoff.Claim{ID: "first", Event: handoff.Event{ID: "first", Work: w.ID, Session: "lead", Text: "Review " + url}, Kind: "proposal", Target: url, Topic: "design", Question: "Review the design?", Quote: "Review " + url}
	id, err := applyHandoff(c, claim)
	if err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	w, err = c.ChangeWork(w, work.Event{Type: "reviewed", Actor: "owner", Target: strings.TrimPrefix(id, "review:")})
	if err != nil {
		t.Fatal(err)
	}
	before := w.Reviews[url]
	claim.ID, claim.Quote, claim.Topic = "recap", "The design is ready for review: "+url, "design proposal"
	claim.Event.ID, claim.Event.Text = "recap", claim.Quote
	if _, err = applyHandoff(c, claim); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	after := w.Reviews[url]
	if after.Version != before.Version || after.State != work.Reviewed || after.StatusError == "" {
		t.Fatalf("unverified recap invented a source change: %+v", after)
	}
}

func TestReadinessHeadRaceRetainsReviewButCannotOfferMerge(t *testing.T) {
	c, w, src := handoffWorld(t)
	src.status.Pending = 0
	c.CheckReview = func(s work.Source) (work.Review, error) {
		return work.Review{ID: s.URL, URL: s.URL, Kind: "PR", Version: "head2:body", Requester: s.Requester, Question: s.Question}, nil
	}
	if _, err := applyHandoff(c, reviewClaim(w, "head-race")); err != nil {
		t.Fatal(err)
	}
	if err := captureMergeReadiness(c); err != nil {
		t.Fatal(err)
	}
	w, _ = c.Store.Work.Get(w.ID)
	url := "https://github.com/example/query/pull/42"
	if !w.Reviews[url].Pending() || w.Reviews[url].Version != "head2:body" || w.Reviews[work.MergeReviewID(url)].Pending() {
		t.Fatalf("mismatched observations granted readiness: %+v", w.Reviews)
	}
	statuses, err := c.Store.PRStatuses()
	if err != nil || statuses[url].ReadyToMerge() || !statuses[url].Fetched.IsZero() {
		t.Fatalf("mismatched cache not due for refresh: %+v %v", statuses[url], err)
	}
}
