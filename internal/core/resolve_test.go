package core

import (
	"errors"
	"fmt"
	"testing"
)

// continuedWorld runs a pane whose conversation "old" continued in "mid", which continued in "new", running as pid 20.
func continuedWorld(t *testing.T, newStatus Status) *world {
	t.Helper()
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "old", Busy)
	w.claude.sessions[10] = Session{ID: "old", Status: Moved, Successor: "mid"}
	w.run(20, 1, "/dev/t2", "claude", "new", newStatus)
	w.claude.next = map[string]string{"old": "mid", "mid": "new"}
	w.tmux.ttys["/dev/t1"] = "%1"
	w.tmux.ttys["/dev/t2"] = "%2"
	return w
}

func TestAnswerReachesTheSessionTheConversationContinuedIn(t *testing.T) {
	for _, c := range []struct {
		name   string
		status Status
		inbox  map[string]error
		want   Delivery
		handed []string
		typed  []string
	}{
		{"its inbox takes it", Busy, map[string]error{"new": nil}, Sent, []string{"new: re D1: go"}, nil},
		{"its inbox fails and it is busy", Busy, map[string]error{"new": errors.New("reset")}, Queued, nil, nil},
		{"no inbox and it is busy", Busy, nil, Queued, nil, nil},
		{"no inbox and it sits idle in tmux", Idle, nil, Typed, nil, []string{"%2: re D1: go"}},
		{"its inbox holds it for the owner while it sits idle", Idle, map[string]error{"new": fmt.Errorf("receipt: %w", ErrPendingApproval)}, Held, nil, nil},
		{"its inbox drops it while it sits idle", Idle, map[string]error{"new": errors.New("dropped")}, Typed, nil, []string{"%2: re D1: go"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := continuedWorld(t, c.status)
			w.claude.inbox = c.inbox
			it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "q", Session: "old"})
			if err != nil {
				t.Fatal(err)
			}
			_, d, err := w.core.Answer(w.board(t), it.ID, "go")
			if err != nil || d != c.want {
				t.Fatalf("delivery = %v, %v; want %v", d, err, c.want)
			}
			if len(w.claude.handed) != len(c.handed) || (len(c.handed) > 0 && w.claude.handed[0] != c.handed[0]) {
				t.Errorf("handed %q, want %q", w.claude.handed, c.handed)
			}
			if len(w.tmux.typed) != len(c.typed) || (len(c.typed) > 0 && w.tmux.typed[0] != c.typed[0]) {
				t.Errorf("typed %q, want %q", w.tmux.typed, c.typed)
			}
			inbox, err := w.core.Inbox("new")
			if err != nil || (len(inbox) == 1) != (c.want == Queued) {
				t.Errorf("successor's inbox = %+v, %v; it should get the answer exactly when nothing delivered it", inbox, err)
			}
			if got, _ := w.core.Store.Item(it.Key()); got.Held.IsZero() == (c.want == Held) {
				t.Errorf("item held at %v, want held %v", got.Held, c.want == Held)
			}
		})
	}
}

func TestContinuedSessionStandsForItsSuccessor(t *testing.T) {
	w := continuedWorld(t, Busy)
	b := w.board(t)
	for _, s := range b.Sessions {
		if s.Key() == "old" {
			t.Fatalf("the moved pane is listed as a live session: %+v", s)
		}
	}
	if s, ok := b.Session("old"); !ok || s.Key() != "new" {
		t.Errorf("Session(old) = %+v, %v; want the successor", s.Key(), ok)
	}
	if b.ProjectOf("new") != "old" {
		t.Errorf("successor's project = %q, want the moved session's", b.ProjectOf("new"))
	}
	for _, ref := range []string{"old", "mid", "new"} {
		c, err := w.core.CallerOf(ref)
		if err != nil || c.Session != "new" || c.PID != 20 {
			t.Errorf("CallerOf(%q) = %+v, %v; want the live successor", ref, c, err)
		}
	}
	if l, err := w.core.Find("old"); err != nil || l.Key() != "new" {
		t.Errorf("Find(old) = %q, %v; want the successor", l.Key(), err)
	}
	for _, filed := range []string{"old", "mid", "new"} {
		it, _ := w.core.AddItem(Item{Kind: KindChore, Question: "q " + filed, Session: filed})
		if _, err := w.core.Store.Transition(it.ID, ItemAnswered, "a"); err != nil {
			t.Fatal(err)
		}
		if _, err := w.core.Store.AddMessage(filed, "m "+filed, MsgPending); err != nil {
			t.Fatal(err)
		}
	}
	it, _ := w.core.AddItem(Item{Kind: KindChore, Question: "elsewhere", Session: "other"})
	w.core.Store.Transition(it.ID, ItemAnswered, "a")
	w.core.Store.AddMessage("other", "m other", MsgPending)
	items, err := w.core.Inbox("new")
	if err != nil || len(items) != 3 {
		t.Fatalf("inbox = %d items, %v; want those filed under every id of the conversation", len(items), err)
	}
	msgs, err := w.core.TakeMessages("new")
	if err != nil || len(msgs) != 3 {
		t.Fatalf("messages = %d, %v; want those sent to every id of the conversation", len(msgs), err)
	}
	if s, _ := w.board(t).Session("new"); len(s.Messages) != 2 || s.Messages[0].Text != "m old" || s.Messages[1].Text != "m new" {
		t.Errorf("board messages = %+v; want the ones keyed to live or moved panes", s.Messages)
	}
}

func TestAnAnswerOnItsWayIsNotAlsoReadFromTheInbox(t *testing.T) {
	for _, c := range []struct {
		name  string
		send  error
		after int
	}{
		{"the session takes it", nil, 0},
		{"the session does not", errors.New("held"), 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := continuedWorld(t, Busy)
			w.claude.inbox = map[string]error{"new": c.send}
			var meanwhile []Item
			w.claude.during = func() { meanwhile, _ = w.core.Inbox("new") }
			it, _ := w.core.AddItem(Item{Kind: KindDecision, Question: "q", Session: "new"})
			if _, _, err := w.core.Answer(w.board(t), it.ID, "go"); err != nil {
				t.Fatal(err)
			}
			if len(meanwhile) != 0 {
				t.Errorf("ym item inbox read %d items while the answer was being sent", len(meanwhile))
			}
			if after, _ := w.core.Inbox("new"); len(after) != c.after {
				t.Errorf("inbox afterwards = %d items, want %d", len(after), c.after)
			}
		})
	}
}
