package tui

import (
	"github.com/project-kikkuli/yamnaya/internal/core"
	"testing"
)

func TestRemoteUsageUnknownAndProfileQualifiedChildSpend(t *testing.T) {
	s := core.Live{Session: core.Session{Harness: "codex", Profile: "work", ID: "parent"}, Remote: "remote-host"}
	m := Model{board: core.Board{Fleet: core.Fleet{Ready: true, Sessions: map[string]core.SessionSpend{}}}}
	if got := m.spend(s); got != "usage unknown" {
		t.Fatalf("old remote snapshot fabricated spend: %q", got)
	}
	if got := m.memberSpend(s, core.Member{ID: "child"}); got != "usage unknown" {
		t.Fatalf("old remote snapshot fabricated child spend: %q", got)
	}
	child := s
	child.ID = "child"
	m.board.Fleet.Sessions[child.UsageKey()] = core.SessionSpend{Total: core.Spend{USD: 7}}
	m.board.Fleet.Sessions["child"] = core.SessionSpend{Total: core.Spend{USD: 99}}
	if got := m.memberSpend(s, core.Member{ID: "child"}); got != "$7.00" {
		t.Fatalf("child spend used another profile/host: %q", got)
	}
}
