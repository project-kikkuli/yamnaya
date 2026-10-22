package codex

import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/drift"
	"github.com/project-kikkuli/yamnaya/internal/health"
)

const scanFormat = 7

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
	// Before is the result as it stood before the latest reply, written at BeforeAt, restored when that reply was a compaction summary.
	Before   string
	BeforeAt time.Time
	// Drift tracks the signs of an agent going nowhere; Calls holds each tool call's name and command until its output arrives.
	Drift drift.Tracker
	Calls map[string]call

	modified time.Time
}

// call is a tool call awaiting its output: what ran, and whether it writes files.
type call struct {
	Tool, Command string
	Writes        bool
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
		t.Facts.Result, t.Facts.ResultAt = t.Before, t.BeforeAt
		t.Drift.Compacted(l.Timestamp)
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
					t.Before, t.BeforeAt = t.Facts.Result, t.Facts.ResultAt
					t.Facts.Result, t.Facts.ResultAt = s, l.Timestamp
					t.Facts.Asked = health.FollowUp(t.Facts.Asked, s)
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
			for _, pr := range core.PRsChanged(p.Input+p.Arguments, cwd) {
				t.Facts.Opened(pr)
			}
			if strings.Contains(p.Input+p.Arguments, "gh pr create") {
				if t.Creating == nil {
					t.Creating = map[string]bool{}
				}
				t.Creating[p.CallID] = true
			}
			if t.Calls == nil {
				t.Calls = map[string]call{}
			}
			cmd := command(p)
			t.Calls[p.CallID] = call{Tool: p.Name, Command: cmd, Writes: len(files) > 0 || len(core.PatchFiles(cmd, "")) > 0}
			t.Drift.Call()
		case "function_call_output", "custom_tool_call_output":
			if c, ok := t.Calls[p.CallID]; ok {
				delete(t.Calls, p.CallID)
				var sig, detail string
				text, failed := outcome(p.Output)
				if failed {
					sig, detail = drift.Failure(c.Tool, c.Command, text)
				}
				t.Drift.Result(sig, detail, p.CallID, l.Timestamp)
				if c.Writes && !failed {
					t.Drift.Wrote()
				}
			}
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
		t.Facts.Ask, t.Facts.Asked = s, ""
		t.Drift.Owner()
	}
}

// command is what a tool call ran: a shell command, an exec script, or "" for other tools.
func command(p payload) string {
	if p.Input != "" {
		return p.Input
	}
	var args struct {
		Command json.RawMessage `json:"command"`
		Cmd     string          `json:"cmd"`
	}
	if json.Unmarshal([]byte(p.Arguments), &args) != nil {
		return ""
	}
	var one string
	if json.Unmarshal(args.Command, &one) == nil {
		return one
	}
	var argv []string
	if json.Unmarshal(args.Command, &argv) == nil {
		return strings.Join(argv, " ")
	}
	return args.Cmd
}

var (
	exited  = regexp.MustCompile(`Process exited with code (\d+)`)
	headers = regexp.MustCompile(`^(Script failed|Chunk ID: \S+|Wall time:? [\d.]+ seconds|Process exited with code \d+|Original token count: \d+|Output:)\s*`)
)

// outcome is a tool output's text without the harness's header lines, and whether it reports a failure.
func outcome(raw json.RawMessage) (string, bool) {
	var text string
	var code *int
	var s string
	if json.Unmarshal(raw, &s) == nil {
		var wrapped struct {
			Output   string `json:"output"`
			Metadata struct {
				ExitCode *int `json:"exit_code"`
			} `json:"metadata"`
		}
		if strings.HasPrefix(s, "{") && json.Unmarshal([]byte(s), &wrapped) == nil {
			text, code = wrapped.Output, wrapped.Metadata.ExitCode
		} else {
			text = s
		}
	} else {
		var parts []struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &parts)
		var all []string
		for _, p := range parts {
			all = append(all, p.Text)
		}
		text = strings.Join(all, "\n")
	}
	head := text[:min(len(text), 200)]
	failed := code != nil && *code != 0 || strings.HasPrefix(head, "Script failed") || strings.HasPrefix(head, "failed in sandbox")
	if m := exited.FindStringSubmatch(head); m != nil && m[1] != "0" {
		failed = true
	}
	for {
		trimmed := headers.ReplaceAllString(strings.TrimLeft(text, " \n"), "")
		if trimmed == text {
			break
		}
		text = trimmed
	}
	return text, failed
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
	out.Facts.Drift = t.Drift.Signs()
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

// Episodes replays a rollout from its start and returns every episode of an agent going nowhere it shows.
func Episodes(path string) []drift.Sign {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	t := scan{V: scanFormat}
	var e drift.Episodes
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var l rolloutLine
		if json.Unmarshal(line, &l) == nil {
			t.add(l, "")
			e.See(t.Drift.Signs())
		}
	}
	return e.List()
}
