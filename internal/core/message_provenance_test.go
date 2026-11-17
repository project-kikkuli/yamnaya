package core

import (
	"testing"
	"time"
)

func TestLegacyMessagesRemainReadableWithoutTerminalReplay(t *testing.T) {
	for _, text := range []string{"ym: rewrite your stale brief", "Claude (mgr10): prepare the next work", "an old message whose author was not recorded"} {
		t.Run(text, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "repair the importer", "/repo/august")
			if _, err := w.core.Store.db.Exec(`INSERT INTO ym_messages(session,text,state,created_at) VALUES(?,?,'pending',?)`, m.Session, text, stamp(w.clock)); err != nil {
				t.Fatal(err)
			}
			b := w.board(t)
			if n, err := w.core.typePending(b, b.Org); err != nil || n != 0 {
				t.Fatalf("legacy replay: %d, %v", n, err)
			}
			if pending := w.core.waitingFor(b.Org, m); len(pending) != 0 {
				t.Fatalf("legacy content became a relaunch prompt: %v", pending)
			}
			messages, err := w.core.TakeMessages(m.Session)
			if err != nil || len(messages) != 1 || messages[0].Text != text || messages[0].Source != "legacy" {
				t.Fatalf("lost legacy inbox history: %v, %v", messages, err)
			}
			if messages, err := w.core.TakeMessages(m.Session); err != nil || len(messages) != 0 {
				t.Fatalf("legacy read was repeated: %v, %v", messages, err)
			}
		})
	}
}

func TestNewOwnerMessagesKeepTheirRecordedProvenance(t *testing.T) {
	w := newWorld(t)
	m, err := w.core.Store.AddMessage("session", "please inspect the failure", MsgPending)
	if err != nil || m.Source != "owner" {
		t.Fatalf("owner message: %+v, %v", m, err)
	}
	if time.Since(m.CreatedAt) > time.Minute {
		t.Fatal("message timestamp lost")
	}
}
