package signals

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	_ "modernc.org/sqlite"
)

// Transcripts finds the session files Claude and Codex append to and reports when each last changed.
type Transcripts struct {
	ClaudeDir string
	CodexHome string

	mu    sync.Mutex
	paths map[string]string
}

func NewTranscripts() *Transcripts {
	home, _ := os.UserHomeDir()
	t := &Transcripts{ClaudeDir: os.Getenv("CLAUDE_CONFIG_DIR"), CodexHome: os.Getenv("CODEX_HOME")}
	if t.ClaudeDir == "" {
		t.ClaudeDir = filepath.Join(home, ".claude")
	}
	if t.CodexHome == "" {
		t.CodexHome = filepath.Join(home, ".codex")
	}
	return t
}

// LastActive is the transcript's modification time, or zero when the transcript is not found.
func (t *Transcripts) LastActive(harnessName, sessionID string) time.Time {
	if sessionID == "" {
		return time.Time{}
	}
	key := harnessName + "/" + sessionID
	t.mu.Lock()
	path := t.paths[key]
	t.mu.Unlock()
	if path != "" {
		if info, err := os.Stat(path); err == nil {
			return info.ModTime()
		}
	}
	path = t.find(harnessName, sessionID)
	if path == "" {
		return time.Time{}
	}
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	t.mu.Lock()
	if t.paths == nil {
		t.paths = map[string]string{}
	}
	t.paths[key] = path
	t.mu.Unlock()
	return info.ModTime()
}

func (t *Transcripts) find(harnessName, sessionID string) string {
	switch harnessName {
	case harness.Claude:
		return newest(filepath.Join(t.ClaudeDir, "projects", "*", sessionID+".jsonl"))
	case harness.Codex:
		if p := t.codexRollout(sessionID); p != "" {
			return p
		}
		return newest(filepath.Join(t.CodexHome, "sessions", "*", "*", "*", "rollout-*-"+sessionID+".jsonl"))
	}
	return ""
}

func (t *Transcripts) codexRollout(threadID string) string {
	matches, _ := filepath.Glob(filepath.Join(t.CodexHome, "state_*.sqlite"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	db, err := sql.Open("sqlite", "file:"+matches[len(matches)-1]+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	if err != nil {
		return ""
	}
	defer db.Close()
	var path string
	if err := db.QueryRow(`SELECT rollout_path FROM threads WHERE id = ?`, threadID).Scan(&path); err != nil {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

func newest(pattern string) string {
	matches, _ := filepath.Glob(pattern)
	var best string
	var bestTime time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err == nil && info.ModTime().After(bestTime) {
			best, bestTime = m, info.ModTime()
		}
	}
	return best
}
