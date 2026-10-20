package work_test

import (
	"fmt"
	"math/rand"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func open(t *testing.T, path string) *core.Store {
	t.Helper()
	s, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func create(t *testing.T, s *core.Store) work.Scope {
	t.Helper()
	w, err := s.Work.Ensure("host/lead", "/src/query/.git", "Query latency")
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func commit(t *testing.T, s *core.Store, w work.Scope, e work.Event) work.Scope {
	t.Helper()
	e.Work, e.Expected = w.ID, w.Revision
	x, err := s.Work.Commit(e)
	if err != nil {
		t.Fatalf("%s: %v", e.Type, err)
	}
	return x
}
func observed(version string, order int64) work.Event {
	return work.Event{Type: "observe", Review: work.Review{ID: "doc", URL: "https://example.test/design", Kind: "document", Title: "Inheritance", Version: version, Order: order, Requester: "Mira", Question: "Accept the inheritance rule?"}}
}
func prepared(version string, context int64) work.Event {
	return work.Event{Type: "prepare", Target: "doc", Actor: "manager", Context: context, Review: work.Review{Version: version, Question: "Accept the inheritance rule?", Recommendation: "Add the nested example", Evidence: "Resolver agrees for two levels", Changed: "Shared resolver added"}}
}

func TestHumanReviewSurvivesFeedbackRevisionAndProcessRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s := open(t, path)
	w := create(t, s)
	w = commit(t, s, w, work.Event{Type: "manager", Target: "manager"})
	w = commit(t, s, w, work.Event{Type: "context", Text: "API and console must agree"})
	w = commit(t, s, w, observed("v3", 3))
	w = commit(t, s, w, prepared("v3", 1))
	if err := s.Work.Seen(w.ID, w.Revision); err != nil {
		t.Fatal(err)
	}
	read, err := s.Work.Get(w.ID)
	if err != nil || read.Reviews["doc"].State != work.Ready {
		t.Fatalf("reading cleared review: %+v %v", read, err)
	}
	w = commit(t, s, w, work.Event{Type: "feedback", Actor: "owner", Target: "doc", Text: "Add a deeply nested example"})
	if w.Reviews["doc"].State != work.WithAuthor {
		t.Fatal("feedback lost next owner")
	}
	id := w.ID
	s.Close()
	s = open(t, path)
	defer s.Close()
	w, err = s.Work.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	w = commit(t, s, w, observed("v4", 4))
	r := w.Reviews["doc"]
	if r.State != work.Preparing || r.Feedback != "Add a deeply nested example" || r.Requester != "Mira" {
		t.Fatalf("revision lost context: %+v", r)
	}
	w = commit(t, s, w, prepared("v4", 1))
	w = commit(t, s, w, work.Event{Type: "decide", Actor: "owner", Target: "doc", Text: "Accept with the verified nested example"})
	if w.Reviews["doc"].State != work.Completed {
		t.Fatal("explicit judgment did not complete review")
	}
	history, err := s.Work.History(id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if history[len(history)-1].Text != "Accept with the verified nested example" {
		t.Fatal("decision rationale not retained")
	}
	updates, err := s.Work.Inbox("manager")
	if err != nil || len(updates) == 0 {
		t.Fatalf("manager lost follow-through: %v", err)
	}
}

func TestStaleJudgmentsAndAuthorityAreRejected(t *testing.T) {
	for _, tc := range []struct {
		name       string
		invalidate work.Event
		judgment   work.Event
	}{
		{"new source", observed("v2", 2), work.Event{Type: "decide", Actor: "owner", Target: "doc", Text: "yes"}},
		{"new context", work.Event{Type: "context", Text: "Preserve the API"}, work.Event{Type: "decide", Actor: "owner", Target: "doc", Text: "yes"}},
		{"agent judgment", work.Event{Type: "seen"}, work.Event{Type: "decide", Actor: "manager", Target: "doc", Text: "yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t, filepath.Join(t.TempDir(), "ym.db"))
			defer s.Close()
			w := create(t, s)
			w = commit(t, s, w, work.Event{Type: "manager", Target: "manager", Actor: "owner"})
			w = commit(t, s, w, observed("v1", 1))
			w = commit(t, s, w, prepared("v1", 0))
			w = commit(t, s, w, tc.invalidate)
			if (tc.invalidate.Type == "observe" || tc.invalidate.Type == "context") && w.Reviews["doc"].State != work.Preparing {
				t.Fatal("changed evidence left a stale judgment in the ready queue")
			}
			tc.judgment.Work, tc.judgment.Expected = w.ID, w.Revision
			if _, err := s.Work.Commit(tc.judgment); err == nil {
				t.Fatal("stale or unauthorized judgment accepted")
			}
			current, _ := s.Work.Get(w.ID)
			if current.Reviews["doc"].State == work.Completed {
				t.Fatal("rejected judgment changed the obligation")
			}
		})
	}
}

func TestDeliveryAndManagerReplacementDoNotImplyContextAdoption(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w := create(t, s)
	w = commit(t, s, w, work.Event{Type: "manager", Target: "old"})
	w = commit(t, s, w, work.Event{Type: "context", Text: "Keep compatibility"})
	w = commit(t, s, w, observed("v1", 1))
	w = commit(t, s, w, work.Event{Type: "manager", Target: "replacement"})
	if err := s.Work.Deliver(func(session, text string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	current, _ := s.Work.Get(w.ID)
	if current.Bindings["replacement"] != 0 {
		t.Fatal("wake receipt was treated as adoption")
	}
	var wg sync.WaitGroup
	received := make(chan []string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			messages, err := s.Work.Inbox("replacement")
			if err != nil {
				t.Error(err)
			}
			received <- messages
		}()
	}
	wg.Wait()
	close(received)
	n := 0
	for messages := range received {
		for _, text := range messages {
			if text != "" {
				n++
			}
		}
	}
	if n != 2 {
		t.Fatalf("pending manager work delivered %d times, want 2", n)
	}
	w = commit(t, s, w, work.Event{Type: "adopt", Actor: "replacement", Context: 1})
	if w.Bindings["replacement"] != 1 {
		t.Fatal("explicit incorporation was not recorded")
	}
}

func TestDuplicateAndOutOfOrderSourceEventsDoNotReopenDecisions(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w := create(t, s)
	e := observed("v2", 2)
	e.ID = "source-v2"
	w = commit(t, s, w, e)
	w = commit(t, s, w, prepared("v2", 0))
	w = commit(t, s, w, work.Event{Type: "decide", Actor: "owner", Target: "doc", Text: "accept"})
	rev := w.Revision
	w = commit(t, s, w, e)
	w = commit(t, s, w, observed("v1", 1))
	if w.Revision != rev || w.Reviews["doc"].State != work.Completed {
		t.Fatal("a repeated or older source changed a recorded decision")
	}
	e.Review.Version = "forged"
	e.Work, e.Expected = w.ID, w.Revision
	if _, err := s.Work.Commit(e); err == nil {
		t.Fatal("event identity was reused for different content")
	}
}

func TestGeneratedReviewHistoriesAgreeWithAnIndependentObligationLedger(t *testing.T) {
	for seed := int64(0); seed < 24; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym.db")
			s := open(t, path)
			defer func() { s.Close() }()
			w := create(t, s)
			w = commit(t, s, w, work.Event{Type: "manager", Target: "manager", Actor: "owner"})
			rng := rand.New(rand.NewSource(seed))
			type fact struct {
				kind             string
				version, context int
			}
			var ledger []fact
			source, contextVersion := 0, 0
			state := func() string {
				if source == 0 {
					return ""
				}
				current := work.Preparing
				for _, f := range ledger {
					if f.kind == "invalidate" {
						current = work.Preparing
					}
					if f.kind == "prepared" && f.version == source && f.context == contextVersion {
						current = work.Ready
					}
					if f.kind == "feedback" {
						current = work.WithAuthor
					}
					if f.kind == "decision" {
						current = work.Completed
					}
				}
				return current
			}
			for step := 0; step < 80; step++ {
				before := state()
				var e work.Event
				allowed := true
				record := fact{}
				switch rng.Intn(7) {
				case 0:
					source++
					e = observed(fmt.Sprintf("v%d", source), int64(source))
					record = fact{kind: "invalidate"}
				case 1:
					contextVersion++
					e = work.Event{Type: "context", Text: fmt.Sprint("context ", contextVersion)}
					if before == work.Ready {
						record = fact{kind: "invalidate"}
					}
				case 2:
					e = prepared(fmt.Sprintf("v%d", source), int64(contextVersion))
					allowed = source > 0 && before != work.Completed
					record = fact{kind: "prepared", version: source, context: contextVersion}
				case 3:
					e = work.Event{Type: "feedback", Actor: "owner", Target: "doc", Text: "revise"}
					allowed = before == work.Ready
					record = fact{kind: "feedback"}
				case 4:
					e = work.Event{Type: "decide", Actor: "owner", Target: "doc", Text: "accept"}
					allowed = before == work.Ready
					record = fact{kind: "decision"}
				case 5:
					e = work.Event{Type: "seen"}
				case 6:
					e = observed(fmt.Sprintf("v%d", source), int64(source))
					allowed = source > 0
					if !allowed {
						e.Review.Version = ""
					}
				}
				e.ID = fmt.Sprintf("%d/%d", seed, step)
				e.Work, e.Expected = w.ID, w.Revision
				next, err := s.Work.Commit(e)
				if (err == nil) != allowed {
					t.Fatalf("seed %d step %d before %s event %+v allowed %v got %v", seed, step, before, e, allowed, err)
				}
				if err == nil {
					w = next
					if record.kind != "" {
						ledger = append(ledger, record)
					}
				}
				actual := ""
				if r, ok := w.Reviews["doc"]; ok {
					actual = r.State
				}
				if actual != state() {
					t.Fatalf("seed %d step %d: obligation ledger %s, storage %s; trace %+v", seed, step, state(), actual, ledger)
				}
				if step%19 == 18 {
					s.Close()
					s = open(t, path)
					w, err = s.Work.Get(w.ID)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
		})
	}
}

func TestConflictingContextEditsKeepTheAcceptedDirection(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w := create(t, s)
	first := work.Event{Work: w.ID, Expected: w.Revision, Type: "context", Text: "keep API stable"}
	second := first
	second.Text = "break the API"
	accepted, err := s.Work.Commit(first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Work.Commit(second); err != work.ErrConflict {
		t.Fatalf("concurrent edit: %v", err)
	}
	actual, _ := s.Work.Get(w.ID)
	if !reflect.DeepEqual(actual.Context, accepted.Context) {
		t.Fatal("stale editor overwrote accepted context")
	}
}

func TestTriggerIsConsumedOnceAndPreservedAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s := open(t, path)
	w := create(t, s)
	at := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	w = commit(t, s, w, work.Event{Type: "trigger", Text: "reassess the migration", WakeAt: at})
	s.Close()
	s = open(t, path)
	defer s.Close()
	w, _ = s.Work.Get(w.ID)
	e := work.Event{Type: "wake", At: at.Add(-time.Second), Work: w.ID, Expected: w.Revision}
	if _, err := s.Work.Commit(e); err == nil {
		t.Fatal("early wake accepted")
	}
	w = commit(t, s, w, work.Event{Type: "wake", At: at})
	if !w.WakeAt.IsZero() {
		t.Fatal("trigger was not consumed")
	}
	if _, err := s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "wake", At: at}); err == nil {
		t.Fatal("consumed trigger woke again")
	}
}

func TestSameTimestampSourceRevisionsRequireTheirObservedPredecessor(t *testing.T) {
	for _, tc := range []struct {
		name, previous string
		accept         bool
	}{{"next snapshot", "v1", true}, {"unordered snapshot", "", false}, {"late competing snapshot", "v0", false}} {
		t.Run(tc.name, func(t *testing.T) {
			s := open(t, filepath.Join(t.TempDir(), "ym.db"))
			defer s.Close()
			w := create(t, s)
			w = commit(t, s, w, observed("v1", 1))
			w = commit(t, s, w, prepared("v1", 0))
			e := observed("v2", 1)
			e.PreviousVersion = tc.previous
			e.Work, e.Expected = w.ID, w.Revision
			next, err := s.Work.Commit(e)
			if (err == nil) != tc.accept {
				t.Fatalf("accept=%v err=%v", tc.accept, err)
			}
			if tc.accept && (next.Reviews["doc"].Version != "v2" || next.Ready() != 0) {
				t.Fatal("new snapshot retained stale preparation")
			}
		})
	}
}

func TestNewManagerReceivesResponsibilityWithoutPreexistingContext(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w := create(t, s)
	w = commit(t, s, w, work.Event{Type: "manager", Target: "new-manager"})
	updates, err := s.Work.Inbox("new-manager")
	if err != nil || len(updates) != 1 {
		t.Fatalf("new manager never received the work: %v %v", updates, err)
	}
	w = commit(t, s, w, work.Event{Type: "relate", Target: "other-repo/work", Text: "depends on schema migration"})
	read, err := s.Work.Get(w.ID)
	if err != nil || read.Related["other-repo/work"] != "depends on schema migration" {
		t.Fatalf("relationship lost: %+v %v", read, err)
	}
}

func TestChangedReviewQuestionInvalidatesItsPreparation(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w := create(t, s)
	address := "https://example.test/design"
	source := observed("v1", 1)
	source.Review.ID = address
	w = commit(t, s, w, source)
	prepare := prepared("v1", 0)
	prepare.Target = address
	w = commit(t, s, w, prepare)
	w = commit(t, s, w, work.Event{Type: "watch", Source: work.Source{URL: address, Requester: "Mira", Question: "Approve the irreversible migration?"}})
	if w.Ready() != 0 || w.Reviews[address].Question != "Approve the irreversible migration?" {
		t.Fatal("changed question retained stale preparation")
	}
}

func TestRichBriefReportsUnavailableSourceCoverage(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w := create(t, s)
	w = commit(t, s, w, work.Event{Type: "brief", Brief: work.Brief{Assessment: "Compatibility holds", Context: 0}})
	if err := s.Work.SourceError(w.ID, "https://example.test/design", fmt.Errorf("document permissions unavailable")); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	work.BriefHandler(s.Work).ServeHTTP(response, httptest.NewRequest("GET", "/brief/"+w.ID, nil))
	if response.Code != 200 || !strings.Contains(response.Body.String(), "document permissions unavailable") || !strings.Contains(response.Body.String(), "Compatibility holds") {
		t.Fatalf("brief hid coverage or lost assessment: %d %s", response.Code, response.Body.String())
	}
}
