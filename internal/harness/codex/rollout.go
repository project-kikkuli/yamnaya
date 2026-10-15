package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const scanFormat = 3

// scan accumulates what a rollout says about its thread, reading only bytes appended since the last read.
// It is saved whole in the scan cache, so a later ym run resumes from Offset.
type scan struct {
	// V is the scan format; a cached scan of another format is read again from the start.
	V        int
	Offset   int64
	Open     bool
	First    string
	Brief    string
	Creating map[string]bool
	Facts    core.Facts
	// Before is the result as it stood before the latest reply, restored when that reply was a compaction summary.
	Before string

	modified time.Time
}

type rolloutLine struct {
	Type      string          `json:"type"`
	Timestamp time.Time       `json:"timestamp"`
	Payload   json.RawMessage `json:"payload"`
}

type payload struct {
	Type      string          `json:"type"`
	Role      string          `json:"role"`
	Message   string          `json:"message"`
	Name      string          `json:"name"`
	Arguments string          `json:"arguments"`
	Input     string          `json:"input"`
	CallID    string          `json:"call_id"`
	Output    json.RawMessage `json:"output"`
	Content   []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}

func (p payload) text(kind string) string {
	var parts []string
	for _, c := range p.Content {
		if c.Type == kind && strings.TrimSpace(c.Text) != "" {
			parts = append(parts, c.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func (p payload) encrypted() bool {
	for _, c := range p.Content {
		if c.Type == "encrypted_content" {
			return true
		}
	}
	return false
}

// typed is a person's prompt, or "" for injected context.
func typed(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "# AGENTS.md") {
		return ""
	}
	return oneLine(t)
}

func (t *scan) add(l rolloutLine, cwd string) {
	var p payload
	if json.Unmarshal(l.Payload, &p) != nil {
		return
	}
	switch l.Type {
	case "compacted":
		t.Facts.Result = t.Before
	case "event_msg":
		switch p.Type {
		case "task_started":
			t.Open = true
		case "task_complete", "turn_aborted":
			t.Open = false
		case "user_message":
			t.prompt(p.Message)
		}
	case "response_item":
		switch p.Type {
		case "message":
			switch p.Role {
			case "user":
				t.prompt(p.text("input_text"))
			case "assistant":
				if s := p.text("output_text"); s != "" {
					t.Before, t.Facts.Result = t.Facts.Result, s
				}
			}
		case "agent_message":
			if t.Brief == "" {
				t.Brief = p.text("input_text")
				if p.encrypted() {
					t.Brief += "\n(the task text is encrypted in the rollout)"
				}
			}
		case "function_call", "custom_tool_call":
			input := json.RawMessage(p.Arguments)
			if p.Type == "custom_tool_call" {
				input, _ = json.Marshal(p.Input)
			}
			a := core.Describe(p.Name, input, cwd, l.Timestamp)
			var files []string
			if p.Name == "apply_patch" || p.Name == "exec" {
				files = core.PatchFiles(p.Input+p.Arguments, cwd)
			}
			t.Facts.Did(a, files...)
			if strings.Contains(p.Input+p.Arguments, "gh pr create") {
				if t.Creating == nil {
					t.Creating = map[string]bool{}
				}
				t.Creating[p.CallID] = true
			}
		case "function_call_output", "custom_tool_call_output":
			if t.Creating[p.CallID] {
				delete(t.Creating, p.CallID)
				for _, pr := range core.PRsIn(string(p.Output)) {
					t.Facts.Opened(pr)
				}
			}
		}
	}
}

func (t *scan) prompt(text string) {
	if s := typed(text); s != "" {
		if t.First == "" {
			t.First = s
		}
		t.Facts.Ask = s
	}
}

func (a *Adapter) scan(path, cwd string) scan {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.scans == nil {
		a.scans = map[string]*scan{}
	}
	t := a.scans[path]
	if t == nil {
		t = &scan{V: scanFormat}
		if a.Cache != nil {
			if off, data, ok := a.Cache.LoadScan(path); ok && json.Unmarshal(data, t) == nil && t.V == scanFormat {
				t.Offset = off
			} else {
				*t = scan{V: scanFormat}
			}
		}
		a.scans[path] = t
	}
	a.advance(t, path, cwd)
	out := *t
	out.Facts.Tools = maps.Clone(t.Facts.Tools)
	return out
}

func (a *Adapter) advance(t *scan, path, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	if info.Size() < t.Offset {
		*t = scan{V: scanFormat}
	}
	t.modified = info.ModTime()
	if info.Size() == t.Offset {
		return
	}
	if _, err := f.Seek(t.Offset, io.SeekStart); err != nil {
		return
	}
	start := t.Offset
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		t.Offset += int64(len(line))
		var l rolloutLine
		if json.Unmarshal(line, &l) == nil {
			t.add(l, cwd)
		}
	}
	if a.Cache != nil && t.Offset != start {
		if data, err := json.Marshal(t); err == nil {
			_ = a.Cache.SaveScan(path, t.Offset, data)
		}
	}
}
