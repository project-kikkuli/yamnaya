package core

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLinearCacheAttentionAndFailedRefresh(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	snap := LinearSnapshot{OwnerID: "owner", WorkspaceID: "workspace", At: at, Issues: []LinearIssue{{ID: "uuid", Identifier: "ENG-1", Assigned: true, State: "Started"}}}
	if err = s.SyncLinear(snap); err != nil {
		t.Fatal(err)
	}
	c := &Core{Store: s}
	b := Board{}
	if err = c.linearNeeds(&b, nil); err != nil {
		t.Fatal(err)
	}
	if len(b.Needs) != 0 {
		t.Fatalf("assigned issues flooded inbox: %+v", b.Needs)
	}
	if err = s.LinearAttention("ENG-1", "choose the rollout"); err != nil {
		t.Fatal(err)
	}
	b = Board{}
	if err = c.linearNeeds(&b, nil); err != nil {
		t.Fatal(err)
	}
	if len(b.Needs) != 1 {
		t.Fatalf("explicit action missing: %+v", b.Needs)
	}
	key, mark := b.Needs[0].Key(), b.Needs[0].MarkKey()
	snap.At = at.Add(time.Hour)
	snap.Issues[0].State = "Done"
	snap.Issues[0].Assigned = false
	if err = s.SyncLinear(snap); err != nil {
		t.Fatal(err)
	}
	i, err := s.LinearIssue("uuid")
	if err != nil {
		t.Fatal(err)
	}
	if i.Attention != "choose the rollout" || i.State != "Done" || i.Assigned {
		t.Fatalf("refresh lost local intent or remote state: %+v", i)
	}
	if (Need{Linear: &i}).Key() != key {
		t.Fatal("ordinary refresh changed attention identity")
	}
	if err = s.LinearFailure(errors.New("offline")); err != nil {
		t.Fatal(err)
	}
	got, err := s.LinearSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if got.Error != "offline" || !got.At.Equal(snap.At) || len(got.Issues) != 1 {
		t.Fatalf("failure erased evidence: %+v", got)
	}
	bad := snap
	bad.Issues = append(bad.Issues, LinearIssue{ID: "invalid"})
	if err = s.SyncLinear(bad); err == nil {
		t.Fatal("accepted incomplete snapshot")
	}
	if err = s.LinearAttention("uuid", "choose the rollout"); err != nil {
		t.Fatal(err)
	}
	i, _ = s.LinearIssue("uuid")
	if (Need{Linear: &i}).Key() != key {
		t.Fatal("identical action resurfaced")
	}
	if err = s.LinearAttention("uuid", "approve launch"); err != nil {
		t.Fatal(err)
	}
	i, _ = s.LinearIssue("uuid")
	n := Need{Linear: &i}
	if n.Key() == key || n.MarkKey() != mark {
		t.Fatal("new action must resurface while preserving snooze identity")
	}
	b = Board{}
	if err = c.linearNeeds(&b, map[string]bool{n.Key(): true}); err != nil {
		t.Fatal(err)
	}
	if len(b.Needs) != 0 {
		t.Fatal("dismissed action resurfaced")
	}
}

func TestLinearRemoteDedupUsesLatestEvidence(t *testing.T) {
	now := time.Now()
	b := Board{Remotes: []RemoteBoard{{Linear: LinearSnapshot{Issues: []LinearIssue{{ID: "same", Identifier: "ENG-2", Title: "fresh", ReadAt: now}}}}}}
	b.attachLinear(LinearSnapshot{Issues: []LinearIssue{{ID: "same", Identifier: "ENG-2", Title: "old", Attention: "choose", AttentionVersion: 2, ReadAt: now.Add(-time.Hour)}, {ID: "other", Identifier: "ENG-1"}}})
	if len(b.Linear.Issues) != 2 || b.Linear.Issues[0].Identifier != "ENG-1" || b.Linear.Issues[1].Title != "fresh" || b.Linear.Issues[1].Attention != "choose" {
		t.Fatalf("bad merged evidence: %+v", b.Linear)
	}
}
