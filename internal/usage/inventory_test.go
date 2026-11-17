package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestInventoryObservesNewAndResumedTranscripts(t *testing.T) {
	for _, harness := range []string{claudeName, codexName} {
		t.Run(harness, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "projects", "project")
			name := "session.jsonl"
			if harness == codexName {
				dir = filepath.Join(root, "sessions", "2026", "11", "16")
				name = "rollout-2026-11-16-aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa.jsonl"
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, name)
			if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			i := sourceInventory{harness: harness, root: root}
			if sources := i.scan(now, false); len(sources) != 1 {
				t.Fatalf("initial sources: %v", sources)
			}
			if err := os.WriteFile(path, []byte("{}\n{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if sources := i.scan(now.Add(time.Second), false); len(sources) != 1 || sources[0].Size != 6 {
				t.Fatalf("live growth not observed: %v", sources)
			}
			newPath := filepath.Join(dir, "new.jsonl")
			if harness == codexName {
				newPath = filepath.Join(dir, "rollout-2026-11-16-bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb.jsonl")
			}
			if err := os.WriteFile(newPath, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if sources := i.scan(now.Add(2*time.Second), false); len(sources) != 2 {
				t.Fatalf("new source not discovered: %v", sources)
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if sources := i.scan(now.Add(3*time.Second), false); len(sources) != 1 || sources[0].Path != newPath {
				t.Fatalf("removed source kept: %v", sources)
			}
		})
	}
}

func TestInventoryKeepsColdHistoryWithoutWalkingItEveryPass(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "projects", "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "old.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cold := now.Add(-2 * coldAfter)
	if err := os.Chtimes(path, cold, cold); err != nil {
		t.Fatal(err)
	}
	i := sourceInventory{harness: claudeName, root: root}
	first := i.scan(now, false)
	if len(first) != 1 {
		t.Fatal(first)
	}
	if err := os.WriteFile(path, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	i.scan(now.Add(time.Second), false)
	if !i.refreshed.Equal(now) {
		t.Fatal("unchanged directory caused a history enumeration")
	}
	// A resumed historical session joins the live set at the bounded cold refresh.
	sources := i.scan(now.Add(coldEvery+time.Second), false)
	if len(sources) != 1 || sources[0].Size != 6 {
		t.Fatalf("resumed history lost: %v", sources)
	}
	if err := os.WriteFile(path, []byte("{}\n{}\n{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if sources := i.scan(now.Add(coldEvery+2*time.Second), false); sources[0].Size != 9 {
		t.Fatalf("resumed session did not stay live: %v", sources)
	}
}
