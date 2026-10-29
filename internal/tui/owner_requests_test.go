package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestItemsShowWhoOwesAnAnswerAndEscalateWithoutDuplicating(t *testing.T) {
	for _, keys := range [][]string{{"e"}, {"enter", "e"}} {
		t.Run(strings.Join(keys, "/"), func(t *testing.T) {
			m, c, _ := setup(t)
			manager, err := c.Store.AddManager(core.Manager{Role: core.RoleChief, Name: "platform"})
			if err != nil {
				t.Fatal(err)
			}
			all, _ := c.Store.Items(core.ItemFilter{})
			it := all[0]
			if err := c.Store.SetRecipient(it.Seq, manager.ID); err != nil {
				t.Fatal(err)
			}
			m = press(t, refresh(t, m), "I")
			m = to(t, m, func(r row) bool { return r.item != nil && r.item.Seq == it.Seq })
			if v := ansi.Strip(m.View().Content); !strings.Contains(v, "to chief platform ("+manager.ID+")") {
				t.Fatalf("recipient is invisible:\n%s", v)
			}
			m = press(t, m, keys...)
			got, err := c.Store.Item(it.Key())
			if err != nil || got.Recipient != core.Owner || got.Seq != it.Seq {
				t.Fatalf("e did not escalate the same item: %+v, %v", got, err)
			}
			items, _ := c.Store.Items(core.ItemFilter{})
			if len(items) != len(all) || !strings.Contains(ansi.Strip(m.View().Content), "to you") {
				t.Fatalf("escalation did not update the list, or duplicated its item:\n%s", ansi.Strip(m.View().Content))
			}
		})
	}
}

func TestReviewDocumentsOpenFromTheQueueAndItemDetail(t *testing.T) {
	for _, detail := range []bool{false, true} {
		t.Run(map[bool]string{false: "queue", true: "detail"}[detail], func(t *testing.T) {
			m, c, _ := setup(t)
			url := "https://claude.ai/artifact/plan"
			it, err := c.AddItem(core.Item{Kind: core.KindReview, Session: "writer", Question: "Review the plan", Rec: "approve", Evidence: []string{url}})
			if err != nil {
				t.Fatal(err)
			}
			opened := ""
			m.openURL = func(url string) error { opened = url; return nil }
			m = to(t, refresh(t, m), func(r row) bool { return r.item != nil && r.item.Seq == it.Seq })
			if detail {
				m = press(t, m, "enter")
			}
			press(t, m, "V")
			if opened != url {
				t.Fatalf("V opened %q, want %q", opened, url)
			}
		})
	}
}

func TestAnOlderOwnerRequestOpensItsManagersCurrentConversation(t *testing.T) {
	for _, role := range []string{core.RoleManager, core.RoleChief} {
		t.Run(role, func(t *testing.T) {
			m, c, host := setup(t)
			manager, err := c.Store.AddManager(core.Manager{Role: role, Name: "platform"})
			if err != nil {
				t.Fatal(err)
			}
			manager.Session = "retired-conversation"
			if err := c.Store.SaveManager(manager); err != nil {
				t.Fatal(err)
			}
			it, err := c.AddOwnerItem(core.Item{Kind: core.KindChore, Session: manager.Session, Harness: "claude", Question: "approve publication"})
			if err != nil {
				t.Fatal(err)
			}
			manager.Session = "lead"
			if err := c.Store.SaveManager(manager); err != nil {
				t.Fatal(err)
			}
			m = to(t, refresh(t, m), func(r row) bool { return r.item != nil && r.item.Seq == it.Seq })
			press(t, m, "o")
			if len(host.focused) != 1 || host.focused[0] != "/dev/a" {
				t.Fatalf("old request did not open current conversation: %v", host.focused)
			}
		})
	}
}
