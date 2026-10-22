package core

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAnIdleAgentsLastMessageReachesItsManagerOnceAcrossARestart(t *testing.T) {
	w := newOrgWorld(t)
	path := filepath.Join(t.TempDir(), "ym.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	w.core.Store = st
	_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	w.spawn("agent", m.Session)
	w.spawn("helper", "agent")
	w.spawn("mine", "")
	at := time.Date(2026, 10, 21, 10, 0, 0, 0, time.UTC)
	reply := "Parser done.\n\nOpen for you\n1. Keep the old CSV dialect?\n2. Drop BOM handling?"
	for _, id := range []string{"agent", "mine", m.Session} {
		w.set(id, func(s *Session) { s.Result, s.ResultAt = reply, at })
	}
	w.set("helper", func(s *Session) { s.Status, s.Result, s.ResultAt = Busy, "still parsing", at })
	forward := func() []string {
		t.Helper()
		w.claude.handed = nil
		for range 3 {
			if err := w.core.Loop(w.board(t)); err != nil {
				t.Fatal(err)
			}
		}
		return w.handedTo(m.Session)
	}
	got := forward()
	if len(got) != 1 || !strings.Contains(got[0], "agent agent") || !strings.Contains(got[0], reply) {
		t.Fatalf("manager got %q, want the agent's whole last message once", got)
	}
	if len(w.claude.handed) != 1 {
		t.Fatalf("handed %q; the owner's own session, the manager and a busy agent forward nothing", w.claude.handed)
	}

	st.Close()
	if w.core.Store, err = OpenStore(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.core.Store.Close() })
	if got := forward(); len(got) != 0 {
		t.Fatalf("restart resent %q", got)
	}
	w.set("agent", func(s *Session) { s.ResultAt = at.Add(time.Hour) })
	if got := forward(); len(got) != 1 {
		t.Fatalf("the same words in a later reply reached the manager %d times, want once", len(got))
	}
}
