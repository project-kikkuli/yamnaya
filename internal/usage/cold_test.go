package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAColdTranscriptIsCountedWithinColdEveryOfGrowing keeps a scan to stat only warm transcripts while a resumed old one still
// counts within coldEvery, and a warm one at once.
func TestAColdTranscriptIsCountedWithinColdEveryOfGrowing(t *testing.T) {
	for _, c := range []struct {
		name string
		age  time.Duration
		by   time.Duration
	}{
		{"warm", time.Minute, 0},
		{"cold", 3 * time.Hour, coldEvery},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "projects", "p", "s1.jsonl")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			old := time.Now().Add(-c.age)
			os.Chtimes(path, old, old)
			now := time.Now()
			clock = func() time.Time { return now }
			t.Cleanup(func() { clock = time.Now })
			size := func() int64 {
				for _, s := range claudeSources(dir) {
					if s.Path == path {
						return s.Size
					}
				}
				return -1
			}
			if got := size(); got != 3 {
				t.Fatalf("first scan size %d", got)
			}
			f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("{}\n")
			f.Close()
			for at := time.Duration(0); at <= c.by; at += 5 * time.Second {
				now = time.Now().Add(at)
				if size() == 6 {
					return
				}
			}
			t.Fatalf("the grown transcript was not counted within %v", c.by)
		})
	}
}
