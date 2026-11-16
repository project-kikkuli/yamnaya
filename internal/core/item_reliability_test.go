package core

import (
	"strings"
	"testing"
	"time"
)

func TestConsolidatedDecisionsResolveForEveryOriginalAsker(t *testing.T) {
	for _, outcome := range []string{ItemAnswered, ItemDismissed} {
		t.Run(outcome, func(t *testing.T) {
			w := newOrgWorld(t)
			_, manager := w.goal(t, "work", "/repo/app")
			chief := w.chief(t, "platform", manager.ID)
			original := w.ask(t, manager.Session, "choose?")
			duplicate := w.ask(t, chief.Session, "choose again?")
			if _, err := w.core.Consolidate(duplicate.Key(), original.Key(), "same decision"); err != nil {
				t.Fatal(err)
			}
			if pending, err := w.core.Store.Items(ItemFilter{Undelivered: true}); err != nil || len(pending) != 0 {
				t.Fatalf("supersession prematurely notified asker: %+v, %v", pending, err)
			}
			if _, err := w.core.Store.Transition(duplicate.Key(), ItemAnswered, "stale reply"); err == nil {
				t.Fatal("reply to superseded decision was accepted")
			}
			if _, err := w.core.Store.Transition(original.Key(), outcome, "choose inside"); err != nil {
				t.Fatal(err)
			}
			for _, ref := range []string{original.Key(), duplicate.Key()} {
				got, err := w.core.Store.Item(ref)
				if err != nil || got.State != outcome || got.Answer+got.Reason != "choose inside" {
					t.Fatalf("resolution %s: %+v, %v", ref, got, err)
				}
			}
			if _, err := w.core.Redeliver(w.board(t)); err != nil {
				t.Fatal(err)
			}
			for _, session := range []string{manager.Session, chief.Session} {
				if !strings.Contains(strings.Join(w.handedTo(session), "\n"), "choose inside") {
					t.Fatalf("%s did not receive the answer", session)
				}
			}
			before := len(w.handedTo(chief.Session))
			if _, err := w.core.Redeliver(w.board(t)); err != nil {
				t.Fatal(err)
			}
			if len(w.handedTo(chief.Session)) != before {
				t.Fatal("answer delivered twice")
			}
		})
	}
}

func TestDeadSupervisorsCannotHideUnresolvedDecisions(t *testing.T) {
	for _, starting := range []bool{false, true} {
		t.Run(map[bool]string{false: "dead", true: "starting"}[starting], func(t *testing.T) {
			w := newOrgWorld(t)
			_, manager := w.goal(t, "work", "/repo/app")
			chief := w.chief(t, "platform", manager.ID)
			w.spawn("worker", manager.Session)
			item := w.ask(t, "worker", "which route?")
			w.stop(manager.Session)
			if !starting {
				w.clock = w.clock.Add(StartGrace + time.Second)
			}
			org, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.reroute(w.board(t), org); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Item(item.Key())
			want := chief.ID
			if starting {
				want = manager.ID
			}
			if err != nil || got.Recipient != want || got.State != ItemOpen {
				t.Fatalf("routed %+v, %v; want %s", got, err, want)
			}
			if !starting && !strings.Contains(got.History[len(got.History)-1].Text, "supervisor unavailable") {
				t.Fatal("rerouting has no durable explanation")
			}
		})
	}
}

func TestConsolidationFlattensCopiesAndSuppressesAnObsoleteAnswer(t *testing.T) {
	w := newOrgWorld(t)
	first := w.ask(t, "first", "choose?")
	second := w.ask(t, "second", "choose again?")
	third := w.ask(t, "third", "final decision?")
	if _, err := w.core.Consolidate(second.Key(), first.Key(), "same"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.Transition(first.Key(), ItemAnswered, "old answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.Transition(first.Key(), ItemOpen, "reconsider"); err != nil {
		t.Fatal(err)
	}
	if pending, err := w.core.Store.Items(ItemFilter{Undelivered: true}); err != nil || len(pending) != 0 {
		t.Fatalf("obsolete answer remains queued: %+v, %v", pending, err)
	}
	if _, err := w.core.Consolidate(first.Key(), third.Key(), "canonical decision"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.Transition(third.Key(), ItemAnswered, "final answer"); err != nil {
		t.Fatal(err)
	}
	pending, err := w.core.Store.Items(ItemFilter{Undelivered: true})
	if err != nil || len(pending) != 3 {
		t.Fatalf("answers: %+v, %v", pending, err)
	}
	for _, it := range pending {
		if it.State != ItemAnswered || it.Answer != "final answer" {
			t.Fatalf("copy lost canonical resolution: %+v", it)
		}
	}
}

func TestReroutingCannotOverwriteAnAnswer(t *testing.T) {
	w := newOrgWorld(t)
	_, manager := w.goal(t, "work", "/repo/app")
	w.spawn("worker", manager.Session)
	item := w.ask(t, "worker", "which route?")
	if _, err := w.core.Store.Transition(item.Key(), ItemAnswered, "settled"); err != nil {
		t.Fatal(err)
	}
	changed, err := w.core.Store.rerouteItem(item, Owner, "supervisor died")
	if err != nil || changed {
		t.Fatalf("stale reroute changed a resolved decision: %v, %v", changed, err)
	}
	got, err := w.core.Store.Item(item.Key())
	if err != nil || got.State != ItemAnswered || got.Answer != "settled" || got.Recipient != item.Recipient {
		t.Fatalf("resolved decision changed: %+v, %v", got, err)
	}
}
