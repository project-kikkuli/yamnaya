package core

import (
	"testing"
	"time"
)

func TestDraftingTellsADeliveryInTheBoxFromTheOwnersTyping(t *testing.T) {
	const delivery = "ym: a delivery from your manager"
	for _, c := range []struct {
		name, box, sent string
		state           string
		age             time.Duration
		want            bool
	}{
		{name: "owner's draft, nothing sent", box: "fix the login page", want: true},
		{name: "owner's draft, a fresh delivery of something else", box: "fix the login page", sent: delivery, state: MsgUncertain, want: true},
		{name: "empty row", box: "fix the login page", sent: "", state: MsgUncertain, want: true},
		{name: "blank row", box: "fix the login page", sent: " \n ", state: MsgUncertain, want: true},
		{name: "empty row under a paste", box: "[Pasted text #1 +30 lines]", sent: "", state: MsgUncertain, want: true},
		{name: "old row", box: delivery, sent: delivery, state: MsgUncertain, age: 6 * time.Minute, want: true},
		{name: "old row under a paste", box: "[Pasted text #1 +30 lines]", sent: delivery, state: MsgUncertain, age: 6 * time.Minute, want: true},
		{name: "fresh delivery", box: delivery, sent: delivery, state: MsgUncertain, age: time.Minute},
		{name: "fresh delivery as a paste", box: "[Pasted text #1 +30 lines]", sent: delivery, state: MsgUncertain, age: time.Minute},
		{name: "a typed row is not on its way", box: delivery, sent: delivery, state: MsgTyped, want: true},
		{name: "empty box", box: "", sent: delivery, state: MsgUncertain},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.run(10, 1, "/dev/t1", "claude", "s", Idle)
			w.tmux.ttys["/dev/t1"] = "%1"
			w.tmux.screens["%1"] = "> " + c.box
			if c.state != "" {
				m, err := w.core.Store.AddMessage("s", c.sent, c.state)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.core.Store.db.Exec(`UPDATE ym_messages SET delivered_at = ? WHERE seq = ?`, time.Now().Add(-c.age).UTC().Format(time.RFC3339Nano), m.Seq); err != nil {
					t.Fatal(err)
				}
			}
			s, ok := w.board(t).Session("s")
			if !ok {
				t.Fatal("no session on the board")
			}
			if got := w.core.Drafting(s); got != c.want {
				t.Fatalf("Drafting = %v, want %v", got, c.want)
			}
		})
	}
}
