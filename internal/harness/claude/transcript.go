package claude

import (
	"bufio"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/drift"
	"github.com/project-kikkuli/yamnaya/internal/health"
)

type record struct {
	Type        string    `json:"type"`
	Subtype     string    `json:"subtype"`
	IsMeta      bool      `json:"isMeta"`
	IsSidechain bool      `json:"isSidechain"`
	CustomTitle string    `json:"customTitle"`
	AgentName   string    `json:"agentName"`
	AITitle     string    `json:"aiTitle"`
	LastPrompt  string    `json:"lastPrompt"`
	PRURL       string    `json:"prUrl"`
	PRNumber    int       `json:"prNumber"`
	PRRepo      string    `json:"prRepository"`
	Cwd         string    `json:"cwd"`
	ContinuedIn string    `json:"continuedInSessionId"`
	Permission  string    `json:"permissionMode"`
	Timestamp   time.Time `json:"timestamp"`
	Origin      *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message *struct {
		ID         string          `json:"id"`
		Model      string          `json:"model"`
		Content    json.RawMessage `json:"content"`
		StopReason *string         `json:"stop_reason"`
		Usage      *struct {
			Input      int64 `json:"input_tokens"`
			CacheWrite int64 `json:"cache_creation_input_tokens"`
			CacheRead  int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (r record) blocks() []block {
	if r.Message == nil {
		return nil
	}
	var bs []block
	if json.Unmarshal(r.Message.Content, &bs) != nil {
		return nil
	}
	return bs
}

// text is the message's plain text: the string content, or its text blocks joined.
func (r record) text() string {
	if r.Message == nil {
		return ""
	}
	var s string
	if json.Unmarshal(r.Message.Content, &s) == nil {
		return s
	}
	var parts []string
	for _, b := range r.blocks() {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "\n")
}

// prompt is the text a person typed, or "" for tool results and injected messages.
func (r record) prompt() string {
	if r.Type != "user" || r.IsMeta || r.IsSidechain || r.Message == nil {
		return ""
	}
	var text string
	if json.Unmarshal(r.Message.Content, &text) != nil {
		bs := r.blocks()
		if len(bs) == 0 || bs[0].Type != "text" {
			return ""
		}
		text = bs[0].Text
	}
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "<") {
		return ""
	}
	return strings.Join(strings.Fields(text), " ")
}

// resultText is a tool result's text, from a string or text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var parts []string
	for _, b := range bs {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

const scanFormat = 8

// scan accumulates what a transcript says about itself, reading only bytes appended since the last read.
// It is saved whole in the scan cache, so a later ym run resumes from Offset.
type scan struct {
	// V is the scan format; a cached scan of another format is read again from the start.
	V         int
	Offset    int64
	Custom    string
	AgentName string
	AI        string
	First     string
	Open      bool
	Brief     string
	// Marked is set once the transcript tags human prompts with an origin; untagged prompts are then injected.
	Marked     bool
	LastPrompt string
	LastType   string
	LastStop   string
	Stopped    bool
	// HandedBack is set while the last assistant record called SubagentHandback, which ends a subagent cleanly.
	HandedBack bool
	LastAt     time.Time
	// UserAt is the latest user record's time; Claude can write a new turn's prompt before the last turn's final reply.
	UserAt time.Time
	// Creating holds tool calls that run gh pr create, until their results arrive.
	Creating map[string]bool
	Facts    core.Facts
	// Next is the session the conversation continued in, until a later turn shows it resumed here.
	Next string
	// Mode is the session's latest permission mode, from its metadata records and the mode stamped on each prompt.
	Mode string
	// Drift tracks the signs of an agent going nowhere; Calls holds each tool call until its result arrives.
	Drift drift.Tracker
	Calls map[string]call

	modified time.Time
	saved    time.Time
	// file is the transcript last read, so a file replaced under the same path is read again from the start.
	file os.FileInfo
}

func (t scan) title() string {
	for _, s := range []string{t.Custom, t.AgentName, t.AI, t.First} {
		if s != "" {
			return s
		}
	}
	return ""
}

// call is a tool call awaiting its result: what ran, and the assistant message that made it.
type call struct {
	Tool, Command, Batch string
}

var wrote = map[string]bool{"Edit": true, "Write": true, "MultiEdit": true, "NotebookEdit": true}

func (t *scan) add(r record) {
	switch r.Type {
	case "custom-title":
		t.Custom = r.CustomTitle
	case "agent-name":
		t.AgentName = r.AgentName
	case "ai-title":
		t.AI = r.AITitle
	case "last-prompt":
		t.LastPrompt = oneLine(r.LastPrompt)
	case "continued-in":
		t.Next = r.ContinuedIn
	case "permission-mode":
		t.Mode = r.Permission
	case "pr-link":
		if r.PRURL != "" {
			t.Facts.Opened(core.PR{URL: r.PRURL, Repo: r.PRRepo, Number: r.PRNumber})
		}
	case "system":
		switch r.Subtype {
		case "turn_duration":
			t.Open = false
		case "compact_boundary":
			t.Drift.Compacted(r.Timestamp)
		}
	case "user", "assistant":
		t.Next = ""
		if r.Permission != "" {
			t.Mode = r.Permission
		}
		t.LastType, t.LastAt = r.Type, r.Timestamp
		if r.Type == "user" && r.Timestamp.After(t.UserAt) {
			t.UserAt = r.Timestamp
		}
		t.LastStop, t.Stopped = "", false
		if r.Message != nil && r.Message.StopReason != nil {
			t.LastStop, t.Stopped = *r.Message.StopReason, true
		}
		if t.Brief == "" && r.Type == "user" {
			t.Brief = strings.TrimSpace(r.text())
		}
		if r.Type == "assistant" {
			t.assistant(r)
		} else {
			t.user(r)
		}
		if r.IsSidechain {
			return
		}
		if u := r.Message; r.Type == "assistant" && u != nil && u.Usage != nil && u.Model != "<synthetic>" {
			t.Facts.Sized(u.Usage.Input+u.Usage.CacheWrite+u.Usage.CacheRead, r.Timestamp)
		}
		if r.Origin != nil {
			t.Marked = true
		}
		if p := r.prompt(); p != "" && (!t.Marked || (r.Origin != nil && r.Origin.Kind == "human")) {
			if t.First == "" {
				t.First = p
			}
			t.Facts.Ask, t.Facts.Asked = p, ""
			t.Drift.Owner()
		}
		t.Open = true
	}
}

func (t *scan) assistant(r record) {
	t.HandedBack = false
	if r.Message != nil && r.Message.Model != "" && !strings.HasPrefix(r.Message.Model, "<") {
		t.Facts.Model = r.Message.Model
	}
	if r.Message != nil && r.Message.Model == "<synthetic>" {
		if text := r.text(); text != "" {
			sig, detail := drift.Failure("harness", "", text)
			t.Drift.Result(sig, detail, r.Timestamp.String(), r.Timestamp)
		}
	}
	for _, b := range r.blocks() {
		switch b.Type {
		case "text":
			if s := strings.TrimSpace(b.Text); s != "" {
				t.Facts.Result, t.Facts.ResultAt = s, r.Timestamp
				t.Facts.Asked = health.FollowUp(t.Facts.Asked, s)
			}
		case "tool_use":
			if b.Name == "SubagentHandback" {
				t.HandedBack = true
			}
			a := core.Describe(b.Name, b.Input, r.Cwd, r.Timestamp)
			var files []string
			if wrote[b.Name] {
				var in struct {
					FilePath     string `json:"file_path"`
					NotebookPath string `json:"notebook_path"`
				}
				_ = json.Unmarshal(b.Input, &in)
				files = append(files, in.FilePath+in.NotebookPath)
			}
			t.Facts.Did(a, files...)
			var in struct {
				Command string `json:"command"`
			}
			_ = json.Unmarshal(b.Input, &in)
			if t.Calls == nil {
				t.Calls = map[string]call{}
			}
			t.Calls[b.ID] = call{Tool: b.Name, Command: in.Command, Batch: r.Message.ID}
			t.Drift.Call()
			if a.Tool == "Bash" && strings.Contains(string(b.Input), "gh pr create") {
				if t.Creating == nil {
					t.Creating = map[string]bool{}
				}
				t.Creating[b.ID] = true
			}
		}
	}
}

func (t *scan) user(r record) {
	if len(t.Creating) == 0 && len(t.Calls) == 0 {
		return
	}
	for _, b := range r.blocks() {
		if b.Type != "tool_result" {
			continue
		}
		if c, ok := t.Calls[b.ToolUseID]; ok {
			if c.Tool == "Bash" && !b.IsError {
				t.Facts.Pushed(c.Command, resultText(b.Content))
			}
			delete(t.Calls, b.ToolUseID)
			var sig, detail string
			if b.IsError {
				sig, detail = drift.Failure(c.Tool, c.Command, resultText(b.Content))
			}
			t.Drift.Result(sig, detail, c.Batch, r.Timestamp)
			if wrote[c.Tool] && !b.IsError {
				t.Drift.Wrote()
			}
		}
		if t.Creating[b.ToolUseID] {
			delete(t.Creating, b.ToolUseID)
			for _, pr := range core.PRsIn(resultText(b.Content)) {
				t.Facts.Opened(pr)
			}
		}
	}
}

// pending reports whether the transcript's last record leaves work outstanding.
func (t scan) pending(quiet time.Duration) bool {
	switch t.LastType {
	case "user":
		return !t.HandedBack
	case "assistant":
		if !t.Stopped {
			return quiet < 2*time.Minute
		}
		return t.LastStop == "tool_use"
	}
	return false
}

// ended reports whether the transcript's last record, written after since and after every user record, is a reply that ended its turn.
func (t scan) ended(since time.Time) bool {
	return t.LastType == "assistant" && t.LastStop == "end_turn" && t.LastAt.After(since) && !t.UserAt.After(t.LastAt)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (a *Adapter) scan(path string) scan {
	if path == "" {
		return scan{}
	}
	t := a.advance(path)
	out := *t
	out.Facts.Tools = maps.Clone(t.Facts.Tools)
	out.Facts.Drift = t.Drift.Signs()
	a.mu.Unlock()
	return out
}

// advance brings the path's scan up to date and returns it with a.mu held.
func (a *Adapter) advance(path string) *scan {
	a.mu.Lock()
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
	info, err := a.stat(path)
	if err != nil {
		return t
	}
	if info.Size() < t.Offset || t.file != nil && !os.SameFile(t.file, info) {
		*t = scan{V: scanFormat}
	}
	if t.file == nil || !t.modified.Equal(info.ModTime()) || info.Size() != t.Offset {
		a.gen.Add(1)
	}
	t.file, t.modified = info, info.ModTime()
	if info.Size() == t.Offset {
		return t
	}
	f, err := os.Open(path)
	if err != nil {
		return t
	}
	defer f.Close()
	if _, err := f.Seek(t.Offset, io.SeekStart); err != nil {
		return t
	}
	start := t.Offset
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		t.Offset += int64(len(line))
		var rec record
		if json.Unmarshal(line, &rec) == nil {
			t.add(rec)
		}
	}
	if a.Cache != nil && t.Offset != start && time.Since(t.saved) >= core.ScanSaveEvery {
		if data, err := json.Marshal(t); err == nil && a.Cache.SaveScan(path, t.Offset, data) == nil {
			t.saved = time.Now()
		}
	}
	return t
}

type agentMeta struct {
	AgentType   string `json:"agentType"`
	Description string `json:"description"`
	Name        string `json:"name"`
	TaskKind    string `json:"taskKind"`
	Model       string `json:"model"`
}

// crew reads <session>/subagents/agent-<id>.meta.json and workflows/<run>/agent-<id>.meta.json, and each agent's transcript.
// Claude marks a session idle only when no delegated work runs, so a session not busy has no busy crew.
func (a *Adapter) crew(dir string, sessionBusy bool, now time.Time) []core.Member {
	metas := a.glob(dir, "agent-*.meta.json")
	for _, run := range a.glob(filepath.Join(dir, "workflows"), "*") {
		metas = append(metas, a.glob(run, "agent-*.meta.json")...)
	}
	var crew []core.Member
	for _, mp := range metas {
		meta, ok := a.meta(mp)
		if !ok {
			continue
		}
		base := strings.TrimSuffix(mp, ".meta.json")
		m := core.Member{
			ID:          strings.TrimPrefix(filepath.Base(base), "agent-"),
			Name:        meta.Name,
			Type:        meta.AgentType,
			Description: meta.Description,
			Teammate:    meta.Name != "" || meta.TaskKind == "in_process_teammate",
		}
		if m.Name == "" {
			m.Name = meta.Description
		}
		if m.Name == "" {
			m.Name = meta.AgentType
		}
		pending := false
		if _, err := a.stat(base + ".jsonl"); err == nil {
			sc := a.scan(base + ".jsonl")
			m.Last, m.Brief, m.Facts = sc.modified, sc.Brief, sc.Facts
			m.Task = firstLine(sc.Brief)
			pending = sc.pending(now.Sub(m.Last))
		}
		if m.Model == "" {
			m.Model = meta.Model
		}
		switch {
		case pending && sessionBusy && now.Sub(m.Last) < staleAfter:
			m.Status = core.Busy
		case m.Teammate:
			m.Status = core.Idle
		default:
			m.Status, m.Cut = core.Done, pending
		}
		crew = append(crew, m)
	}
	sort.SliceStable(crew, func(i, j int) bool {
		if (crew[i].Status == core.Busy) != (crew[j].Status == core.Busy) {
			return crew[i].Status == core.Busy
		}
		return crew[i].Last.After(crew[j].Last)
	})
	return crew
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// Episodes replays a transcript from its start and returns every episode of an agent going nowhere it shows.
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
		var rec record
		if json.Unmarshal(line, &rec) == nil {
			t.add(rec)
			e.See(t.Drift.Signs())
		}
	}
	return e.List()
}
