package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPassiveRemoteOutcomeKeepsFullEvidenceWithoutActionPriority(t *testing.T) {
	payload := strings.Repeat("full command outcome with retained evidence ", 40) + "final marker"
	event := GoalEvent{Seq: 9, Kind: EventRelayed, Payload: payload}
	status := quietRollup([]GoalEvent{event}, func(GoalEvent) string { return "a/passive" }, "box")
	if len(status["a/passive"].Actions) != 0 {
		t.Fatal("passive outcome acquired action priority")
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var received Rollup
	if err := json.Unmarshal(raw, &received); err != nil {
		t.Fatal(err)
	}
	received = received.merged(status)
	received["z/actionable"] = GoalDigest{Actions: []string{"answer the unresolved ask"}}
	lines := received.lines()
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "z/actionable:") {
		t.Fatalf("passive detail outranked actual action: %q", lines)
	}
	if strings.Count(lines[1], "#box/9 relayed: "+payload) != 1 {
		t.Fatalf("full outcome lost or duplicated in serialized status: %q", lines[1])
	}
}
