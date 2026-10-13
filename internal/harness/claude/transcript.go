package claude

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type record struct {
	Type        string `json:"type"`
	Subtype     string `json:"subtype"`
	IsMeta      bool   `json:"isMeta"`
	IsSidechain bool   `json:"isSidechain"`
	CustomTitle string `json:"customTitle"`
	AgentName   string `json:"agentName"`
	AITitle     string `json:"aiTitle"`
	Message     *struct {
		Content    json.RawMessage `json:"content"`
		StopReason *string         `json:"stop_reason"`
	} `json:"message"`
}

// prompt is the text a person typed, or "" for tool results and injected messages.
func (r record) prompt() string {
	if r.Type != "user" || r.IsMeta || r.IsSidechain || r.Message == nil {
		return ""
	}
	var text string
	if json.Unmarshal(r.Message.Content, &text) != nil {
		var blocks []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(r.Message.Content, &blocks) != nil || len(blocks) == 0 || blocks[0].Type != "text" {
			return ""
		}
		text = blocks[0].Text
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

// scan accumulates what a transcript says about itself, reading only bytes appended since the last read.
type scan struct {
	offset    int64
	custom    string
	agentName string
	ai        string
	first     string
	open      bool
	modified  time.Time
}

func (t scan) title() string {
	for _, s := range []string{t.custom, t.agentName, t.ai, t.first} {
		if s != "" {
			return s
		}
	}
	return ""
}

func (t *scan) add(r record) {
	switch r.Type {
	case "custom-title":
		t.custom = r.CustomTitle
	case "agent-name":
		t.agentName = r.AgentName
	case "ai-title":
		t.ai = r.AITitle
	case "system":
		if r.Subtype == "turn_duration" {
			t.open = false
		}
	case "user", "assistant":
		if r.IsSidechain {
			return
		}
		if t.first == "" {
			t.first = r.prompt()
		}
		t.open = true
	}
}

func (a *Adapter) scan(path string) scan {
	if path == "" {
		return scan{}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.scans == nil {
		a.scans = map[string]*scan{}
	}
	t := a.scans[path]
	if t == nil {
		t = &scan{}
		a.scans[path] = t
	}
	f, err := os.Open(path)
	if err != nil {
		return *t
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return *t
	}
	if info.Size() < t.offset {
		*t = scan{}
	}
	t.modified = info.ModTime()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return *t
	}
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		t.offset += int64(len(line))
		var rec record
		if json.Unmarshal(line, &rec) == nil {
			t.add(rec)
		}
	}
	return *t
}

// lastRecord parses the final complete line of a JSONL file.
func lastRecord(path string) (record, bool) {
	f, err := os.Open(path)
	if err != nil {
		return record{}, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return record{}, false
	}
	start := max(info.Size()-1<<16, 0)
	buf := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && err != io.EOF {
		return record{}, false
	}
	lines := bytes.Split(bytes.TrimRight(buf, "\n"), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		var r record
		if json.Unmarshal(lines[i], &r) == nil {
			return r, true
		}
	}
	return record{}, false
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Name        string `json:"name"`
	TaskKind    string `json:"taskKind"`
}

// crew reads <session>/subagents/agent-<id>.meta.json and each agent's transcript.
// Claude marks a session idle only when no delegated work runs, so a session not busy has no busy crew.
func (a *Adapter) crew(dir string, sessionBusy bool, now time.Time) []core.Member {
	metas, _ := filepath.Glob(filepath.Join(dir, "agent-*.meta.json"))
	type member struct {
		core.Member
		last time.Time
	}
	var crew []member
	for _, mp := range metas {
		b, err := os.ReadFile(mp)
		if err != nil {
			continue
		}
		var meta agentMeta
		if json.Unmarshal(b, &meta) != nil {
			continue
		}
		m := member{Member: core.Member{Name: meta.Name, Teammate: meta.Name != "" || meta.TaskKind == "in_process_teammate"}}
		if m.Name == "" {
			m.Name = meta.Description
		}
		if m.Name == "" {
			m.Name = meta.AgentType
		}
		transcript := strings.TrimSuffix(mp, ".meta.json") + ".jsonl"
		pending := false
		if info, err := os.Stat(transcript); err == nil {
			m.last = info.ModTime()
			if r, ok := lastRecord(transcript); ok {
				pending = midStep(r, now.Sub(m.last))
			}
		}
		switch {
		case pending && sessionBusy && now.Sub(m.last) < staleAfter:
			m.Status = core.Busy
		case m.Teammate:
			m.Status = core.Idle
		default:
			m.Status = core.Done
		}
		crew = append(crew, m)
	}
	sort.SliceStable(crew, func(i, j int) bool {
		if (crew[i].Status == core.Busy) != (crew[j].Status == core.Busy) {
			return crew[i].Status == core.Busy
		}
		return crew[i].last.After(crew[j].last)
	})
	out := make([]core.Member, len(crew))
	for i, m := range crew {
		out[i] = m.Member
	}
	return out
}

// midStep reports whether an agent's last record leaves work outstanding.
func midStep(r record, quiet time.Duration) bool {
	switch r.Type {
	case "user":
		return true
	case "assistant":
		if r.Message == nil || r.Message.StopReason == nil {
			return quiet < 2*time.Minute
		}
		return *r.Message.StopReason == "tool_use"
	}
	return false
}
