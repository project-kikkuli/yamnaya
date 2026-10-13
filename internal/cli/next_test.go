package cli

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/store"
)

func TestNextCyclesWindowsByOldestItem(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha", "w1", "w2")
	e.project("beta", "b1")
	e.item("beta", "b1", store.KindDecision)
	e.item("alpha", "w2", store.KindDecision)
	e.item("alpha", "", store.KindChore)
	e.item("alpha", "w2", store.KindDefault)
	e.attach("ym-alpha")

	want := []string{
		e.target("beta", "b1"),
		e.target("alpha", "w2"),
		e.target("alpha", "manager"),
		e.target("beta", "b1"),
	}
	for i, w := range want {
		e.ym("next")
		if got := e.clientWindow(); got != w {
			t.Fatalf("jump %d: client on %s, want %s", i+1, got, w)
		}
	}
}

func TestNextSkipsDeadSourcesAndAnsweredItems(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha", "w1", "w2")
	gone := e.item("alpha", "w1", store.KindDecision)
	done := e.item("alpha", "w2", store.KindDecision)
	if _, err := e.a.store.Answer("alpha", done.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	e.tmux("kill-window", "-t", e.target("alpha", "w1"))
	e.attach("ym-alpha")
	if got := e.ym("next"); got != "nothing asks you" {
		t.Fatalf("ym next = %q with only a dead source (%s) open", got, gone.ID)
	}
	e.item("alpha", "w2", store.KindDecision)
	e.ym("next")
	if got := e.clientWindow(); got != e.target("alpha", "w2") {
		t.Fatalf("client on %s, want w2", got)
	}
}

func TestNextVisitsPermissionPromptsBeforeOpenItems(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha", "w1")
	e.project("beta", "b1")
	e.item("alpha", "w1", store.KindDecision)
	e.showDialog(e.target("beta", "b1"), "codex-exec")
	e.attach("ym-alpha")
	for i, w := range []string{e.target("beta", "b1"), e.target("alpha", "w1"), e.target("beta", "b1")} {
		e.ym("next")
		if got := e.clientWindow(); got != w {
			t.Fatalf("jump %d: client on %s, want %s", i+1, got, w)
		}
	}
}
