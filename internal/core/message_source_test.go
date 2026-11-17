package core

import (
	"strings"
	"testing"
)

func TestSystemResultsRemainReadableWithoutOwnerReplay(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "repair the importer", "/repo/august")
	msg, err := w.core.Store.AddSystemMessage(m.Session, "remote command completed")
	if err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	if n, err := w.core.typePending(b, b.Org); err != nil || n != 0 {
		t.Fatalf("system result replayed: %d %v", n, err)
	}
	if waiting := w.core.waitingFor(b.Org, m); len(waiting) != 0 {
		t.Fatalf("tool result became a resume instruction: %+v", waiting)
	}
	messages, err := w.core.TakeMessages(m.Session)
	if err != nil || len(messages) != 1 || messages[0].Source != "system" {
		t.Fatalf("tool result unreadable: %+v %v", messages, err)
	}
	for _, text := range []string{w.core.prompt(m, b.Org, []Message{msg}), w.core.resumeText(b.Org, m, []Message{msg}, nil), w.core.restartText(b, b.Org, m, restart{}, []Message{msg}, nil)} {
		if !strings.Contains(text, "[system tool result] remote command completed") || strings.Contains(text, "[owner] remote command completed") {
			t.Fatalf("prompt relabeled source: %s", text)
		}
	}
}

func TestUnknownMessageSourcesNeverClaimOwnerAuthority(t *testing.T) {
	for _, source := range []string{"", "legacy", "unrecognized"} {
		if label := (Message{Source: source}).SourceLabel(); label != "legacy, source unknown" {
			t.Fatalf("unknown source %q became %q", source, label)
		}
	}
}
