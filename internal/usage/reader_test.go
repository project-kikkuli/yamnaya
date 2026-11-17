package usage

import (
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
