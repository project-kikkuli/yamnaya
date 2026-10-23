package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// An idle session's later records reach the board: one written while its idle state settles at the next read, any other within
// idleRecheck, and a turn that changes the state file at once.
func TestAnIdleSessionsLaterRecordsReachTheBoardInTime(t *testing.T) {
	for _, c := range []struct {
		name string
		// after is when the record lands past the state file's write; seen is the latest read that must show it.
		after, seen time.Duration
		restate     bool
	}{
		{"final reply as the state settles", 300 * time.Millisecond, time.Second, false},
		{"title long after", 3 * time.Minute, 3*time.Minute + idleRecheck, false},
		{"next turn rewrites the state file", 3 * time.Minute, 3*time.Minute + time.Second, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			state := filepath.Join(dir, "sessions", "7.json")
			transcript := filepath.Join(dir, "projects", "-Users-me-app", sid+".jsonl")
			must(t, os.MkdirAll(filepath.Dir(state), 0o755))
			must(t, os.MkdirAll(filepath.Dir(transcript), 0o755))
			write(t, state, `{"pid":7,"sessionId":"`+sid+`","cwd":"/Users/me/app","status":"idle","statusUpdatedAt":1}`)
			write(t, transcript, `{"type":"user","message":{"role":"user","content":"Write the notes"}}`+"\n")
			info, err := os.Stat(state)
			must(t, err)
			wrote := info.ModTime()
			now := wrote
			a := &Adapter{Dir: dir, Now: func() time.Time { return now }}
			title := func() string { return a.Discover([]proc.Proc{p(7, "claude")})[0].Title }
			for at := time.Duration(0); at < c.after; at += time.Second {
				now = wrote.Add(at)
				title()
			}
			f, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0)
			must(t, err)
			_, err = fmt.Fprintln(f, `{"type":"custom-title","customTitle":"Release notes"}`)
			must(t, err)
			must(t, f.Close())
			if c.restate {
				write(t, state, `{"pid":7,"sessionId":"`+sid+`","cwd":"/Users/me/app","status":"idle","statusUpdatedAt":2}`)
			}
			for at := c.after; at <= c.seen; at += time.Second {
				now = wrote.Add(at)
				if title() == "Release notes" {
					return
				}
			}
			t.Fatalf("the record written %v after the state file was not on the board by %v", c.after, c.seen)
		})
	}
}
