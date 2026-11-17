package usage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUsageReadersObserveCollectorCommitsWithoutReadingNewTranscriptLines(t *testing.T) {
	collector := meter(t)
	path := filepath.Join(collector.ClaudeDir, "projects", "project", parentID+".jsonl")
	write(t, path, reply("one", "claude-sonnet-5", 10, 20, 0, 0, 0))
	scan(t, collector)
	reader := NewReader(collector.StateDir)
	reader.ClaudeDir, reader.CodexHome = collector.ClaudeDir, collector.CodexHome
	t.Cleanup(func() { reader.Close() })
	check := func(want int64) {
		t.Helper()
		f, err := reader.Read(ts)
		if err != nil || f.Today.Tokens.Input != want {
			t.Fatalf("persisted usage %+v, %v; want input %d", f.Today.Tokens, err, want)
		}
		if len(reader.inventories) != 0 {
			t.Fatal("reader enumerated transcripts")
		}
	}
	check(10)
	write(t, path, reply("two", "claude-sonnet-5", 20, 30, 0, 0, 0))
	check(10)
	var responses int
	if err := reader.ledger.db.QueryRow(`SELECT count(*) FROM ym_usage_responses`).Scan(&responses); err != nil {
		t.Fatal(err)
	}
	if responses != 1 {
		t.Fatal("reader wrote uncollected usage")
	}
	scan(t, collector)
	check(30)
}

func TestUsageReaderDoesNotInitializeOrPruneCollectorState(t *testing.T) {
	state := t.TempDir()
	reader := NewReader(state)
	f, err := reader.Read(ts)
	if err != nil || f.Ready || !f.Read {
		t.Fatalf("uncollected usage presented as current: %+v, %v", f, err)
	}
	reader.Close()
	entries, err := os.ReadDir(state)
	if err != nil || len(entries) != 0 {
		t.Fatalf("reader initialized collector state: %v, %v", entries, err)
	}
	collector := meter(t)
	path := filepath.Join(collector.ClaudeDir, "projects", "project", parentID+".jsonl")
	write(t, path, reply("one", "claude-sonnet-5", 10, 20, 0, 0, 0))
	scan(t, collector)
	if _, err := collector.ledger.db.Exec(`DROP TABLE ym_usage_revision`); err != nil {
		t.Fatal(err)
	}
	if _, err := collector.ledger.db.Exec(`INSERT INTO ym_usage_slices SELECT bucket-1000000,file,model,tier,fast,us,input,output,cache_read,cache_5m,cache_1h FROM ym_usage_slices`); err != nil {
		t.Fatal(err)
	}
	legacy := NewReader(collector.StateDir)
	t.Cleanup(func() { legacy.Close() })
	f, err = legacy.Read(ts)
	if err != nil || f.Today.Tokens.Input != 10 {
		t.Fatalf("legacy accounting unavailable: %+v, %v", f.Today, err)
	}
	var revisions, oldSlices int
	if err := collector.ledger.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name='ym_usage_revision'`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if err := collector.ledger.db.QueryRow(`SELECT count(*) FROM ym_usage_slices WHERE bucket<?`, bucketOf(ts.Add(-keepSlices).Unix())).Scan(&oldSlices); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 || oldSlices != 1 {
		t.Fatalf("read mutated collector state: revision tables %d, old slices %d", revisions, oldSlices)
	}
}
