package tui

import (
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestChatActionShowsWhereToActAndOpensItsSession(t *testing.T) {
	m, c, h := setup(t)
	it, err := c.AddItem(core.Item{Scope: "repo", Session: "lead", Harness: "claude", Kind: core.KindChore, Action: "chat", Question: "Enter the native login code", Assumption: "Agent helper requested this action in its parent conversation."})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m = press(t, to(t, m, func(r row) bool { return r.need != nil && r.need.Item != nil && r.need.Item.Session == "lead" }), "enter")
	m = to(t, m, func(r row) bool { return r.item != nil && r.item.Key() == it.Key() })
	if !strings.Contains(m.View(), "action in chat") || !strings.Contains(strings.Join(m.footer(), "\n"), "o open chat") {
		t.Fatalf("native destination unclear: %s", m.View())
	}
	press(t, m, "o")
	if len(h.focused) != 1 || h.focused[0] != "/dev/a" {
		t.Fatalf("wrong chat opened: %+v", h.focused)
	}
	latest, _ := c.Store.Item(it.Key())
	if latest.State != core.ItemOpen || latest.Answer != "" {
		t.Fatal("opening chat recorded an approval or completed an action")
	}
}
