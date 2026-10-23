package usage

import (
	"bytes"
	"encoding/json"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const (
	claudeName = "claude"
	codexName  = "codex"
)

// source is one transcript file and the session it counts toward.
type source struct {
	PhysicalPath string
	EpochStart   int64
	ConfigRoot   string
	Profile      string
	ProfileLabel string
	Account      string
	Path         string
	Harness      string
	Session      string
	// Root marks the session's own transcript, as opposed to a subagent's.
	Root  bool
	Size  int64
	MTime int64
}

// claudeSources lists projects/<p>/<session>.jsonl and every agent-*.jsonl under projects/<p>/<session>/subagents.
func claudeSources(dir string) []source {
	var out []source
	for _, p := range readDir(filepath.Join(dir, "projects")) {
		if !p.IsDir() {
			continue
		}
		pdir := filepath.Join(dir, "projects", p.Name())
		for _, e := range readDir(pdir) {
			name := e.Name()
			if !e.IsDir() {
				if strings.HasSuffix(name, ".jsonl") {
					if s, ok := stat(filepath.Join(pdir, name), strings.TrimSuffix(name, ".jsonl"), true); ok {
						out = append(out, s)
					}
				}
				continue
			}
			sub := filepath.Join(pdir, name, "subagents")
			walk(sub, func(path string, d fs.DirEntry) {
				base := d.Name()
				if strings.HasPrefix(base, "agent-") && strings.HasSuffix(base, ".jsonl") {
					if s, ok := stat(path, name, false); ok {
						out = append(out, s)
					}
				}
			})
		}
	}
	return out
}

func stat(path, session string, root bool) (source, bool) {
	info, err := lstat(path)
	if err == nil && strings.HasSuffix(path, ".zst") {
		info, err = nativefile.Stat(path)
	}
	if err != nil {
		return source{}, false
	}
	return source{Path: path, Session: session, Root: root, Size: info.Size(), MTime: info.ModTime().UnixNano()}, true
}

// response is one model response, which Claude Code writes once per content block with repeated usage.
type response struct {
	ID    string
	Model string
	Day   string
	// At is when the harness logged it, in Unix seconds.
	At     int64
	Class  class
	Tokens core.Tokens
}

type claudeLine struct {
	Type         string    `json:"type"`
	Timestamp    time.Time `json:"timestamp"`
	TotalCostUSD float64   `json:"totalCostUSD"`
	Message      struct {
		ID    string `json:"id"`
		Model string `json:"model"`
		Usage *struct {
			Input         int64 `json:"input_tokens"`
			Output        int64 `json:"output_tokens"`
			CacheRead     int64 `json:"cache_read_input_tokens"`
			CacheCreation int64 `json:"cache_creation_input_tokens"`
			Split         *struct {
				M5 int64 `json:"ephemeral_5m_input_tokens"`
				H1 int64 `json:"ephemeral_1h_input_tokens"`
			} `json:"cache_creation"`
			Speed string `json:"speed"`
			Geo   string `json:"inference_geo"`
		} `json:"usage"`
	} `json:"message"`
}

var (
	costState     = []byte(`"type":"cost-state"`)
	assistantType = []byte(`"type":"assistant"`)
	usageKey      = []byte(`"usage":{`)
)

// claudeChunk is what a run of new transcript lines adds.
type claudeChunk struct {
	responses []response
	cost      float64
}

func parseClaude(line []byte, c *claudeChunk) {
	if head := line[:min(len(line), 40)]; bytes.Contains(head, costState) {
		var r claudeLine
		if json.Unmarshal(line, &r) == nil && r.TotalCostUSD > c.cost {
			c.cost = r.TotalCostUSD
		}
		return
	}
	if !bytes.Contains(line, usageKey) || !bytes.Contains(line, assistantType) {
		return
	}
	var r claudeLine
	if json.Unmarshal(line, &r) != nil || r.Type != "assistant" || r.Message.Usage == nil || r.Message.ID == "" || r.Message.Model == "<synthetic>" {
		return
	}
	u := r.Message.Usage
	t := core.Tokens{Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, CacheWrite5m: u.CacheCreation}
	if u.Split != nil && u.Split.M5+u.Split.H1 > 0 {
		t.CacheWrite5m, t.CacheWrite1h = u.Split.M5, u.Split.H1
	}
	c.responses = append(c.responses, response{
		ID:     r.Message.ID,
		Model:  r.Message.Model,
		Day:    day(r.Timestamp),
		At:     r.Timestamp.Unix(),
		Class:  class{Tier: tierOf(u.Input + u.CacheRead + u.CacheCreation), Fast: u.Speed == "fast", US: u.Geo == "us"},
		Tokens: t,
	})
}

// most keeps the larger of each count: later content blocks of a response carry its final usage.
func most(a, b core.Tokens) core.Tokens {
	return core.Tokens{Input: max(a.Input, b.Input), Output: max(a.Output, b.Output), CacheRead: max(a.CacheRead, b.CacheRead),
		CacheWrite5m: max(a.CacheWrite5m, b.CacheWrite5m), CacheWrite1h: max(a.CacheWrite1h, b.CacheWrite1h)}
}

func minus(a, b core.Tokens) core.Tokens {
	return core.Tokens{Input: a.Input - b.Input, Output: a.Output - b.Output, CacheRead: a.CacheRead - b.CacheRead,
		CacheWrite5m: a.CacheWrite5m - b.CacheWrite5m, CacheWrite1h: a.CacheWrite1h - b.CacheWrite1h}
}
