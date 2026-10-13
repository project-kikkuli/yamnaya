package discover

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// claudeState is the per-process file Claude Code keeps in sessions/<pid>.json while it runs.
type claudeState struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	NameSource      string `json:"nameSource"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

func (d *Discoverer) claudeState(pid int) (claudeState, bool) {
	b, err := os.ReadFile(filepath.Join(d.ClaudeDir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return claudeState{}, false
	}
	var st claudeState
	if json.Unmarshal(b, &st) != nil || st.PID != pid || st.SessionID == "" {
		return claudeState{}, false
	}
	return st, true
}

func (d *Discoverer) hasClaudeState(pid int) bool {
	_, ok := d.claudeState(pid)
	return ok
}

var nonPathChars = regexp.MustCompile(`[^A-Za-z0-9]`)

func encodeCwd(cwd string) string { return nonPathChars.ReplaceAllString(cwd, "-") }

func argSessionID(args []string) string {
	for i, a := range args {
		for _, flag := range []string{"--resume", "-r", "--session-id"} {
			if a == flag && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1]
			}
			if strings.HasPrefix(a, flag+"=") {
				return strings.TrimPrefix(a, flag+"=")
			}
		}
	}
	return ""
}

func (d *Discoverer) claudeTranscript(cwd, id string) string {
	p := filepath.Join(d.ClaudeDir, "projects", encodeCwd(cwd), id+".jsonl")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	matches, _ := filepath.Glob(filepath.Join(d.ClaudeDir, "projects", "*", id+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// newestSince is the transcript in dir last written at or after since, for a process with no session id on record.
func newestSince(dir string, since time.Time) string {
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var best string
	var bestTime time.Time
	for _, m := range matches {
		info, err := os.Stat(m)
		if err == nil && !info.ModTime().Before(since) && info.ModTime().After(bestTime) {
			best, bestTime = m, info.ModTime()
		}
	}
	return best
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func (d *Discoverer) readClaude(s *Session, p Proc, started, now time.Time) {
	st, ok := d.claudeState(p.PID)
	if ok {
		s.ID = st.SessionID
		if st.Cwd != "" {
			s.Cwd = st.Cwd
		}
	}
	if s.ID == "" {
		s.ID = argSessionID(p.Args)
	}
	var path string
	if s.ID != "" {
		path = d.claudeTranscript(s.Cwd, s.ID)
	} else if path = newestSince(filepath.Join(d.ClaudeDir, "projects", encodeCwd(s.Cwd)), started); path != "" {
		s.ID = strings.TrimSuffix(filepath.Base(path), ".jsonl")
	}
	scan := d.scanTranscript(path)
	s.Title = scan.title()
	if s.Title == "" && ok && st.NameSource != "derived" {
		s.Title = st.Name
	}
	switch {
	case ok && st.Status == "busy":
		s.Status, s.Since = Running, msTime(st.StatusUpdatedAt)
	case ok && st.Status == "waiting":
		s.Status, s.Since, s.WaitingFor = NeedsYou, msTime(st.StatusUpdatedAt), st.WaitingFor
	case ok && st.Status == "idle":
		s.Status, s.Since = Idle, msTime(st.StatusUpdatedAt)
	case scan.open && now.Sub(scan.modified) < StaleAfter:
		s.Status, s.Since = Running, scan.modified
	default:
		s.Since = scan.modified
		s.Status = settle(s.Since, now)
	}
	if path != "" {
		s.Crew = d.claudeCrew(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents"), s.Status == Running, now)
	}
}

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

// prompt is the text a person typed, or "" for tool results and harness-injected messages.
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

// titleScan accumulates what a transcript says about itself, reading only bytes appended since the last scan.
type titleScan struct {
	offset    int64
	custom    string
	agentName string
	ai        string
	first     string
	open      bool
	modified  time.Time
}

func (t titleScan) title() string {
	for _, s := range []string{t.custom, t.agentName, t.ai, t.first} {
		if s != "" {
			return s
		}
	}
	return ""
}

func (t *titleScan) add(r record) {
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

type codexRecord struct {
	Type    string `json:"type"`
	Payload struct {
		Type string `json:"type"`
	} `json:"payload"`
}

func (t *titleScan) addCodex(r codexRecord) {
	if r.Type != "event_msg" {
		return
	}
	switch r.Payload.Type {
	case "task_started":
		t.open = true
	case "task_complete", "turn_aborted":
		t.open = false
	}
}

func (d *Discoverer) scanTranscript(path string) titleScan { return d.scan(path, false) }

func (d *Discoverer) scan(path string, codex bool) titleScan {
	if path == "" {
		return titleScan{}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.titles == nil {
		d.titles = map[string]*titleScan{}
	}
	t := d.titles[path]
	if t == nil {
		t = &titleScan{}
		d.titles[path] = t
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
		*t = titleScan{}
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
		if codex {
			var rec codexRecord
			if json.Unmarshal(line, &rec) == nil {
				t.addCodex(rec)
			}
		} else {
			var rec record
			if json.Unmarshal(line, &rec) == nil {
				t.add(rec)
			}
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
