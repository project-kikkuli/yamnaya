package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestSupervisorActionsWaitForCapacityAndNeverReplayAcceptedWork(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "repair importer", "/repo/august")
	var submitted []string
	w.core.BackgroundSend = func(s Live, text string) error { submitted = append(submitted, text); return nil }
	for range 3 {
		if err := w.core.requestAction(w.board(t), m.Session, "directive", "repair", "Inspect the parser failure and repair it"); err != nil {
			t.Fatal(err)
		}
	}
	w.set(m.Session, func(s *Session) { s.Status = Busy })
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if len(submitted) != 0 {
		t.Fatal("submitted while supervisor was busy")
	}
	w.set(m.Session, func(s *Session) { s.Status = Idle })
	for range 3 {
		if err := w.core.DispatchActions(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	if len(submitted) != 1 || !strings.Contains(submitted[0], "repair it") {
		t.Fatalf("submissions: %q", submitted)
	}
	actions, err := w.core.Store.Activations()
	if err != nil || len(actions) != 1 || actions[0].State != "sent" {
		t.Fatalf("actions: %+v, %v", actions, err)
	}
	if err := w.core.CompleteAction(actions[0].ID, "different-session", "fixed"); err == nil {
		t.Fatal("another session completed the action")
	}
	if err := w.core.CompleteAction(actions[0].ID, m.Session, "parser repaired"); err != nil {
		t.Fatal(err)
	}
	if err := w.core.CompleteAction(actions[0].ID, m.Session, "parser repaired"); err != nil {
		t.Fatal(err)
	}
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if len(submitted) != 1 {
		t.Fatal("completed work replayed")
	}
}

func TestRejectedAndUncertainActionsNeedExplicitRecovery(t *testing.T) {
	for _, outcome := range []struct {
		name, state string
		err         error
	}{
		{"unsupported", "blocked", errors.New("native control unavailable")},
		{"lost receipt", "uncertain", fmt.Errorf("lost native receipt: %w", ErrSubmissionUncertain)},
	} {
		t.Run(outcome.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "repair importer", "/repo/august")
			attempts := 0
			w.core.BackgroundSend = func(Live, string) error { attempts++; return outcome.err }
			for _, id := range []string{"one", "two", "three"} {
				if err := w.core.requestAction(w.board(t), m.Session, "directive", id, "repair "+id); err != nil {
					t.Fatal(err)
				}
			}
			for range 4 {
				if err := w.core.DispatchActions(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			if attempts != 1 {
				t.Fatalf("control retried without approval: %d", attempts)
			}
			actions, err := w.core.Store.Activations()
			if err != nil {
				t.Fatal(err)
			}
			var failed Activation
			for _, a := range actions {
				if a.State == outcome.state {
					failed = a
				}
			}
			if failed.ID == "" || failed.Attention == 0 {
				t.Fatalf("recovery disappeared: %+v", actions)
			}
			if err := w.core.actionAttention(failed, "still unavailable"); err != nil {
				t.Fatal(err)
			}
			items, err := w.core.Store.Items(ItemFilter{States: []string{ItemOpen}})
			if err != nil || len(items) != 1 {
				t.Fatalf("recovery inbox flooded: %v, %v", items, err)
			}
			if err := w.core.RetryAction(failed.ID); err != nil {
				t.Fatal(err)
			}
			w.core.BackgroundSend = func(Live, string) error { attempts++; return nil }
			if err := w.core.DispatchActions(w.board(t)); err != nil {
				t.Fatal(err)
			}
			if attempts != 2 {
				t.Fatalf("explicit retry did not dispatch: %d", attempts)
			}
			if err := w.core.CompleteAction(failed.ID, m.Session, "fixed"); err != nil {
				t.Fatal(err)
			}
			item, err := w.core.Store.Item(fmt.Sprintf("#%d", failed.Attention))
			if err != nil || item.State != ItemDismissed {
				t.Fatalf("recovery item remained after completion: %v, %v", item, err)
			}
		})
	}
}

func TestBookkeepingAndResolvedEvidenceNeverActivateAModel(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "repair importer", "/repo/august")
	for _, kind := range []string{EventIdleAgain, EventUpdate, EventPROpened, EventCIGreen, EventMerged, EventDone, EventVerified, EventAdopted, EventRefreshed, EventResumed, EventMachineThrottled, EventNudge} {
		t.Run(kind, func(t *testing.T) {
			needed, err := w.core.eventActionNeeded(w.board(t), w.board(t).Org, GoalEvent{Goal: g.ID, Kind: kind, Payload: "status changed"})
			if err != nil || needed {
				t.Fatalf("observation requires model: %v, %v", needed, err)
			}
		})
	}
	url := "https://github.com/acme/august/pull/9"
	w.pr(t, m.Session, url, PRStatus{State: "OPEN", Failed: 1, Merge: PRMergeFacts{OID: "a"}})
	e, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "CI failed on "+url, g.ID+":ci-red:"+url+"@a")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.core.requestAction(w.board(t), m.Session, "event", fmt.Sprint(e.Seq), "Fix failed CI"); err != nil {
		t.Fatal(err)
	}
	w.pr(t, m.Session, url, PRStatus{State: "OPEN", Passed: 1, Merge: PRMergeFacts{OID: "b"}})
	calls := 0
	w.core.BackgroundSend = func(Live, string) error { calls++; return nil }
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	actions, err := w.core.Store.Activations()
	if err != nil || calls != 0 || len(actions) != 1 || actions[0].State != "superseded" {
		t.Fatalf("resolved evidence activated model: %d, %+v, %v", calls, actions, err)
	}
}

func TestInFlightSubmissionIsNotRetriedOrPrematurelyEscalated(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "repair importer", "/repo/august")
	if err := w.core.requestAction(w.board(t), m.Session, "directive", "one", "repair the parser"); err != nil {
		t.Fatal(err)
	}
	actions, _ := w.core.Store.Activations()
	a := actions[0]
	if _, err := w.core.Store.db.Exec(`UPDATE ym_activations SET state='claimed',target_session=?,dispatched=? WHERE id=?`, m.Session, stamp(w.clock), a.ID); err != nil {
		t.Fatal(err)
	}
	w.core.BackgroundSend = func(Live, string) error { t.Fatal("replayed in-flight action"); return nil }
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	items, _ := w.core.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if len(items) != 0 {
		t.Fatal("in-flight submission escalated before its receipt")
	}
	w.clock = w.clock.Add(2 * time.Minute)
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	items, _ = w.core.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if len(items) != 1 {
		t.Fatal("abandoned claim did not remain visible for recovery")
	}
}
