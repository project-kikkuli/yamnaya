package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestConcreteActionsShareOneNativeTurnWithoutLosingTheirReceipts(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "repair importer", "/repo/august")
	for n := range 17 {
		if err := w.core.requestAction(w.board(t), m.Session, "directive", fmt.Sprint(n), fmt.Sprintf("repair parser %d", n)); err != nil {
			t.Fatal(err)
		}
	}
	var turns []string
	w.core.BackgroundSend = func(_ Live, text string) error { turns = append(turns, text); return nil }
	for range 2 {
		if err := w.core.DispatchActions(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	if len(turns) != 1 || strings.Count(turns[0], "Yamnaya supervisor action ") != 16 {
		t.Fatalf("turns: %d, actions in first: %d", len(turns), strings.Count(strings.Join(turns, ""), "Yamnaya supervisor action "))
	}
	actions, err := w.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	accepted := 0
	for _, a := range actions {
		if a.State != "sent" {
			continue
		}
		accepted++
		if a.Attempt != 1 || a.Batch == "" {
			t.Fatalf("submission identity missing: %+v", a)
		}
		if err := w.core.CompleteAction(a.ID, m.Session, "fixed "+a.Subject); err != nil {
			t.Fatal(err)
		}
	}
	if accepted != 16 {
		t.Fatalf("accepted %d", accepted)
	}
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 || strings.Count(turns[1], "Yamnaya supervisor action ") != 1 {
		t.Fatalf("remaining work: %q", turns)
	}
}

func TestAnUncertainBatchRetainsOneRecoveryCaseAndRetriesWithNewAttempts(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "repair importer", "/repo/august")
	for n := range 3 {
		if err := w.core.requestAction(w.board(t), m.Session, "directive", fmt.Sprint(n), fmt.Sprintf("repair %d", n)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	w.core.BackgroundSend = func(Live, string) error { calls++; return fmt.Errorf("lost receipt: %w", ErrSubmissionUncertain) }
	for range 3 {
		if err := w.core.DispatchActions(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("uncertain submission replayed: %d", calls)
	}
	items, err := w.core.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if err != nil || len(items) != 1 {
		t.Fatalf("recovery cases: %+v, %v", items, err)
	}
	actions, err := w.core.Store.Activations()
	if err != nil || len(actions) != 3 {
		t.Fatalf("actions: %+v, %v", actions, err)
	}
	if err := w.core.RetryAction(actions[0].ID); err != nil {
		t.Fatal(err)
	}
	w.core.BackgroundSend = func(_ Live, text string) error {
		calls++
		if strings.Count(text, "(attempt 2,") != 3 {
			return errors.New("retry omitted members or reused an old attempt")
		}
		return nil
	}
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("explicit batch retry did not dispatch: %d", calls)
	}
	actions, err = w.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.State != "sent" || a.Attempt != 2 {
			t.Fatalf("retry lost identity: %+v", a)
		}
	}
	items, err = w.core.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if err != nil || len(items) != 0 {
		t.Fatalf("recovered connection remained in owner inbox: %+v, %v", items, err)
	}
}
