package core

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQueuedAnswersReachTheirSessionOnceItIsReachable(t *testing.T) {
	reset := errors.New("reset")
	for _, c := range []struct {
		name string
		// then is what the successor's inbox does by the next refresh; absent means it is not running yet at the answer.
		at, then  map[string]error
		running   bool
		want      int
		handed    []string
		delivered bool
	}{
		{"its inbox failed and now takes it", map[string]error{"new": reset}, map[string]error{"new": nil}, true, 1, []string{"new: re D1: go"}, true},
		{"it started after the answer", nil, map[string]error{"new": nil}, false, 1, []string{"new: re D1: go"}, true},
		{"its inbox still fails", map[string]error{"new": reset}, map[string]error{"new": reset}, true, 0, nil, false},
		{"it held the answer for the owner", map[string]error{"new": fmt.Errorf("receipt: %w", ErrPendingApproval)}, map[string]error{"new": nil}, true, 0, nil, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.run(10, 1, "/dev/t1", "claude", "old", Busy)
			w.claude.sessions[10] = Session{ID: "old", Status: Moved, Successor: "new"}
			w.claude.next = map[string]string{"old": "new"}
			if c.running {
				w.run(20, 1, "/dev/t2", "claude", "new", Busy)
			}
			w.claude.inbox = c.at
			it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "q", Session: "old"})
			if err != nil {
				t.Fatal(err)
			}
			if _, d, err := w.core.Answer(w.board(t), it.ID, "go"); err != nil || d == Sent {
				t.Fatalf("answer = %v, %v; the first delivery should not reach the session", d, err)
			}
			if !c.running {
				w.run(20, 1, "/dev/t2", "claude", "new", Busy)
			}
			w.claude.inbox = c.then
			for i, want := range []int{c.want, 0} {
				n, err := w.core.Redeliver(w.board(t))
				if err != nil || n != want {
					t.Fatalf("refresh %d: Redeliver = %d, %v; want %d", i+1, n, err, want)
				}
			}
			if fmt.Sprint(w.claude.handed) != fmt.Sprint(c.handed) {
				t.Errorf("handed %q, want %q", w.claude.handed, c.handed)
			}
			if got, _ := w.core.Store.Item(it.Key()); got.Delivered.IsZero() == c.delivered {
				t.Errorf("delivered at %v, want delivered %v", got.Delivered, c.delivered)
			}
		})
	}
}

func TestQueuedAnswersReachASessionAsOneNotice(t *testing.T) {
	w := newWorld(t)
	var ids []string
	for _, q := range []string{"q1", "q2", "q3"} {
		it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: q, Session: "s"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
	}
	if _, _, err := w.core.Answer(w.board(t), ids[0], "go"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.core.Dismiss(w.board(t), ids[1], "stale"); err != nil {
		t.Fatal(err)
	}
	w.run(20, 1, "/dev/t2", "claude", "s", Busy)
	w.claude.inbox = map[string]error{"s": nil}
	for i, want := range []int{2, 0} {
		if n, err := w.core.Redeliver(w.board(t)); err != nil || n != want {
			t.Fatalf("refresh %d: Redeliver = %d, %v; want %d", i+1, n, err, want)
		}
	}
	want := fmt.Sprintf("[s: re %s: go; re %s: dismissed: stale]", ids[0], ids[1])
	if fmt.Sprint(w.claude.handed) != want {
		t.Errorf("handed %q, want %s", w.claude.handed, want)
	}
}

func TestPendingMessagesAreTypedOnceTheirSessionSitsIdleAtAnEmptyPrompt(t *testing.T) {
	for _, c := range []struct {
		name    string
		status  Status
		box     string
		pane    bool
		age     time.Duration
		typed   []string
		pending int
		logged  string
	}{
		{"idle at an empty prompt", Idle, "> ", true, time.Minute, []string{"%2: first\n\nsecond"}, 0, ""},
		{"busy", Busy, "> ", true, time.Minute, nil, 2, ""},
		{"idle with the owner's draft in the box", Idle, "> half a thought", true, time.Minute, nil, 2, ""},
		{"behind a permission dialog", Idle, "DIALOG Bash", true, time.Minute, nil, 2, ""},
		{"older than an hour", Idle, "> ", true, 2 * time.Hour, nil, 2, ""},
		{"idle with no terminal ym can type into", Idle, "> ", false, time.Minute, nil, 2, "2 messages for s not delivered: no terminal ym can type into"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.run(20, 1, "/dev/t2", "claude", "s", c.status)
			if c.pane {
				w.tmux.ttys["/dev/t2"] = "%2"
				w.tmux.screens["%2"] = c.box
			}
			var logged []string
			w.core.Log = func(line string) { logged = append(logged, line) }
			for _, text := range []string{"first", "second"} {
				if _, err := w.core.Store.AddMessage("s", text, MsgPending); err != nil {
					t.Fatal(err)
				}
			}
			at := time.Now().Add(c.age)
			w.core.Now = func() time.Time { return at }
			for range 2 {
				if _, err := w.core.Redeliver(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			if fmt.Sprint(w.tmux.typed) != fmt.Sprint(c.typed) {
				t.Errorf("typed %q, want %q", w.tmux.typed, c.typed)
			}
			if left, _ := w.core.Store.PendingMessages(); len(left) != c.pending {
				t.Errorf("%d messages still pending, want %d", len(left), c.pending)
			}
			var want []string
			if c.logged != "" {
				want = []string{c.logged}
			}
			if fmt.Sprint(logged) != fmt.Sprint(want) {
				t.Errorf("logged %q, want %q once per message", logged, want)
			}
		})
	}
}

func TestAnAnswerThatCannotReachAnIdleAgentSaysWhyOnce(t *testing.T) {
	w := newWorld(t)
	w.run(20, 1, "/dev/t2", "claude", "s", Idle)
	var logged []string
	w.core.Log = func(line string) { logged = append(logged, line) }
	it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "q", Session: "s"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.core.Answer(w.board(t), it.ID, "go"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := w.core.Redeliver(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "answer to "+it.ID+" for s not delivered: no terminal ym can type into") {
		t.Fatalf("logged %q, want one line saying why", logged)
	}
	w.tmux.ttys["/dev/t2"] = "%2"
	w.tmux.screens["%2"] = "> "
	if n, err := w.core.Redeliver(w.board(t)); err != nil || n != 1 {
		t.Fatalf("Redeliver once a terminal shows = %d, %v; want 1", n, err)
	}
}
