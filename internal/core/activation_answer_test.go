package core

import "testing"

func TestAQueuedSupervisorAnswerRemainsReadableUntilNativeReceipt(t *testing.T) {
	for _, consumeInbox := range []bool{false, true} {
		t.Run(map[bool]string{false: "native receipt", true: "inbox read"}[consumeInbox], func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "repair importer", "/repo/august")
			w.spawn("worker", m.Session)
			it := w.ask(t, "worker", "which parser should I use?")
			if _, _, err := w.core.Answer(w.board(t), it.Key(), "use the streaming parser"); err != nil {
				t.Fatal(err)
			}
			stored, err := w.core.Store.Item(it.Key())
			if err != nil || !stored.Delivered.IsZero() {
				t.Fatalf("queued answer disappeared from inbox: %+v, %v", stored, err)
			}
			calls := 0
			w.core.BackgroundSend = func(s Live, _ string) error {
				if s.ID == "worker" {
					calls++
				}
				return nil
			}
			if consumeInbox {
				items, err := w.core.Inbox("worker")
				if err != nil || len(items) != 1 || items[0].Answer != "use the streaming parser" {
					t.Fatalf("inbox: %+v, %v", items, err)
				}
			}
			for range 3 {
				if err := w.core.DispatchActions(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			want := 1
			if consumeInbox {
				want = 0
			}
			if calls != want {
				t.Fatalf("native activations %d, want %d", calls, want)
			}
			stored, err = w.core.Store.Item(it.Key())
			if err != nil || stored.Delivered.IsZero() {
				t.Fatalf("receipt not recorded: %+v, %v", stored, err)
			}
		})
	}
}

func TestReopeningAnItemSupersedesItsQueuedAnswer(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "repair importer", "/repo/august")
	w.spawn("worker", m.Session)
	it := w.ask(t, "worker", "which parser?")
	if _, _, err := w.core.Answer(w.board(t), it.Key(), "obsolete answer"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.Transition(it.Key(), ItemOpen, ""); err != nil {
		t.Fatal(err)
	}
	w.core.BackgroundSend = func(s Live, _ string) error {
		if s.ID == "worker" {
			t.Fatal("obsolete answer activated the worker")
		}
		return nil
	}
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	actions, err := w.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range actions {
		if a.Kind == "answer" && a.State != "superseded" {
			t.Fatalf("obsolete answer remained pending: %+v", a)
		}
	}
}
