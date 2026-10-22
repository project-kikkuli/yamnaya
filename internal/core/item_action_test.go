package core

import (
	"path/filepath"
	"testing"
)

func TestItemActionUpgradeRetainsOwnerAnswersAndPersistsChatRequests(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	it, err := s.AddItem(Item{Scope: "repo", Session: "lead", Kind: KindGo, Question: "Start the scoped change?"})
	if err != nil {
		t.Fatal(err)
	}
	it, err = s.Transition(it.Key(), ItemAnswered, "Proceed with the scoped change")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`ALTER TABLE ym_items DROP COLUMN action`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	old, err := s.Item(it.Key())
	if err != nil || old.Answer != it.Answer || old.State != ItemAnswered || old.Action != "" {
		t.Fatalf("legacy owner record changed: %+v %v", old, err)
	}
	chat, _, err := s.addItem(Item{Scope: "repo", Session: "lead", Harness: "codex", Kind: KindChore, Action: "chat", Question: "Enter the native login code"}, "native-login")
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	same, _, err := s.addItem(Item{Scope: "repo", Session: "lead", Kind: KindChore, Action: "chat", Question: "Enter the native login code"}, "native-login")
	if err != nil || same.Key() != chat.Key() || same.Action != "chat" {
		t.Fatalf("restart lost chat identity: %+v %v", same, err)
	}
}
