package health

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Wait kinds: an agent ended its turn asking the owner, or a tool call sat on a permission prompt.
const (
	WaitAsk        = "ask"
	WaitPermission = "permission"
)

// state is what reading a transcript must remember between reads.
type state struct {
	// Skip marks a file that never gets a typed message: a Codex exec run or child thread.
	Skip bool `json:"skip,omitempty"`
	// Seen is set once the first Codex session_meta line was read.
	Seen bool `json:"seen,omitempty"`
	// Marked is set once a Claude transcript tags typed prompts with an origin.
	Marked bool `json:"marked,omitempty"`
	InTurn bool `json:"in_turn,omitempty"`
	// Asked is whether the turn's latest assistant text holds questions for the owner.
	Asked bool `json:"asked,omitempty"`
	// Filed is whether the turn filed a ym item or asked through a question tool.
	Filed bool      `json:"filed,omitempty"`
	Last  time.Time `json:"last"`
	// WaitFrom is when the agent started waiting on the owner; zero when it is not.
	WaitFrom time.Time `json:"wait_from"`
	// Paused is when a message that is not the owner's woke the agent during a wait.
	Paused time.Time `json:"paused"`
}

type reprint struct {
	Key string
	Day string
}

type wait struct {
	Kind       string
	Start, End time.Time
}

// found is what a run of new lines adds.
type found struct {
	reprints []reprint
	waits    []wait
}

func (s *state) assistant(at time.Time, text string, hasText bool, f *found) {
	s.InTurn, s.Last = true, at
	if !hasText {
		return
	}
	s.Asked = Asks(text)
	if s.Asked {
		f.reprints = append(f.reprints, reprint{Key: key(at.UTC().Format(time.RFC3339Nano), text), Day: day(at)})
	}
}

// endTurn starts a wait when the turn ended asking the owner. A wait the turn interrupted ends where the
// turn began; one with no interruption ends here if the turn no longer asks.
func (s *state) endTurn(f *found) {
	if !s.InTurn {
		return
	}
	if !s.WaitFrom.IsZero() && !s.Paused.IsZero() {
		f.add(WaitAsk, s.WaitFrom, s.Paused)
		s.WaitFrom = time.Time{}
	}
	waiting := s.Asked || s.Filed
	switch {
	case waiting && s.WaitFrom.IsZero():
		s.WaitFrom = s.Last
	case !waiting && !s.WaitFrom.IsZero():
		f.add(WaitAsk, s.WaitFrom, s.Last)
		s.WaitFrom = time.Time{}
	}
	s.InTurn, s.Asked, s.Filed, s.Paused = false, false, false, time.Time{}
}

// human ends any wait at the owner's typed message.
func (s *state) human(at time.Time, f *found) {
	s.endTurn(f)
	if !s.WaitFrom.IsZero() {
		f.add(WaitAsk, s.WaitFrom, at)
		s.WaitFrom = time.Time{}
	}
	s.Paused = time.Time{}
}

// injected pauses a wait at a message that is not the owner's; the turn it starts decides whether the wait resumes.
func (s *state) injected(at time.Time, f *found) {
	s.endTurn(f)
	if !s.WaitFrom.IsZero() && s.Paused.IsZero() {
		s.Paused = at
	}
}

func (f *found) add(kind string, start, end time.Time) {
	if start.IsZero() || !end.After(start) {
		return
	}
	f.waits = append(f.waits, wait{Kind: kind, Start: start, End: end})
}

func key(parts ...string) string {
	h := sha1.Sum([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(h[:12])
}

func day(t time.Time) string { return t.Local().Format(time.DateOnly) }

var (
	tsKey    = []byte(`"timestamp":"`)
	fileItem = []byte("ym item add")
)

// stamp reads the first timestamp in a line without decoding it.
func stamp(line []byte) time.Time {
	i := bytes.Index(line, tsKey)
	if i < 0 {
		return time.Time{}
	}
	rest := line[i+len(tsKey):]
	j := bytes.IndexByte(rest, '"')
	if j < 0 {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, string(rest[:j]))
	return t
}

type claudeRecord struct {
	Type        string    `json:"type"`
	Subtype     string    `json:"subtype"`
	IsMeta      bool      `json:"isMeta"`
	IsSidechain bool      `json:"isSidechain"`
	Timestamp   time.Time `json:"timestamp"`
	Origin      *struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message *struct {
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type claudeBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var (
	userType      = []byte(`"type":"user"`)
	assistantType = []byte(`"type":"assistant"`)
	turnDuration  = []byte(`"subtype":"turn_duration"`)
	toolResult    = []byte(`"tool_result"`)
	userDecision  = []byte(`"permissionDecision":{"decision":"`)
	decidedByUser = []byte(`","source":"user`)
	userRejected  = []byte(`The user doesn't want to proceed with this tool use`)
	textBlock     = []byte(`"type":"text"`)
	toolUse       = []byte(`"type":"tool_use"`)
	sidechainTrue = []byte(`"isSidechain":true`)
)

// claudeLine reads one line of a Claude Code session transcript.
func (s *state) claudeLine(line []byte, f *found) {
	switch {
	case bytes.Contains(line, turnDuration):
		s.endTurn(f)
	case bytes.Contains(line, assistantType):
		if bytes.Contains(line[:min(len(line), 120)], sidechainTrue) {
			return
		}
		if bytes.Contains(line, fileItem) && bytes.Contains(line, toolUse) {
			s.Filed = true
		}
		if !bytes.Contains(line, textBlock) {
			if at := stamp(line); !at.IsZero() {
				s.InTurn, s.Last = true, at
			}
			return
		}
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil || r.Type != "assistant" || r.IsSidechain || r.Message == nil || r.Message.Model == "<synthetic>" {
			return
		}
		var bs []claudeBlock
		json.Unmarshal(r.Message.Content, &bs)
		var parts []string
		for _, b := range bs {
			if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
				parts = append(parts, b.Text)
			}
		}
		s.assistant(r.Timestamp, strings.Join(parts, "\n"), len(parts) > 0, f)
	case bytes.Contains(line, userType):
		if bytes.Contains(line, toolResult) {
			if i := bytes.Index(line, userDecision); (i >= 0 && bytes.Contains(line[i:min(len(line), i+60)], decidedByUser)) || bytes.Contains(line, userRejected) {
				f.add(WaitPermission, s.Last, stamp(line))
			}
			return
		}
		var r claudeRecord
		if json.Unmarshal(line, &r) != nil || r.Type != "user" || r.IsSidechain || r.Message == nil {
			return
		}
		if r.Origin != nil {
			s.Marked = true
		}
		if typed(r, s.Marked) {
			s.human(r.Timestamp, f)
		} else if !r.IsMeta {
			s.injected(r.Timestamp, f)
		}
	}
}

// typed reports whether a user record is a prompt the owner typed: tagged human, or before tagging existed, untagged text that is not injected.
func typed(r claudeRecord, marked bool) bool {
	if marked {
		return r.Origin != nil && r.Origin.Kind == "human"
	}
	if r.IsMeta {
		return false
	}
	var text string
	if json.Unmarshal(r.Message.Content, &text) != nil {
		var bs []claudeBlock
		if json.Unmarshal(r.Message.Content, &bs) != nil || len(bs) == 0 || bs[0].Type != "text" {
			return false
		}
		text = bs[0].Text
	}
	text = strings.TrimSpace(text)
	return text != "" && !strings.HasPrefix(text, "<")
}

type codexRecord struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Payload   struct {
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Name    string          `json:"name"`
		Source  json.RawMessage `json:"source"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"payload"`
}

var (
	sessionMeta  = []byte(`"type":"session_meta"`)
	responseItem = []byte(`"type":"response_item"`)
	taskComplete = []byte(`"type":"task_complete"`)
	askTool      = []byte(`"name":"request_user_input`)
	roleUser     = []byte(`"role":"user"`)
	roleAgent    = []byte(`"role":"assistant"`)
)

// harness marks user-role Codex messages the harness adds, not the owner.
var harness = []string{"# AGENTS.md", "<environment_context", "<subagent_notification", "<user_instructions", "<turn_aborted",
	"<permissions", "<skill", "<collaboration_mode", "Another Claude session sent a message"}

// codexLine reads one line of a Codex rollout; only threads started from the CLI or the app carry typed messages.
func (s *state) codexLine(line []byte, f *found) {
	if s.Skip {
		return
	}
	head := line[:min(len(line), 160)]
	if !s.Seen {
		if !bytes.Contains(head, sessionMeta) {
			return
		}
		s.Seen = true
		var r codexRecord
		if json.Unmarshal(line, &r) != nil {
			s.Skip = true
			return
		}
		var src string
		s.Skip = json.Unmarshal(r.Payload.Source, &src) != nil || (src != "cli" && src != "vscode")
		return
	}
	switch {
	case bytes.Contains(head, taskComplete):
		s.Last = stamp(line)
		s.endTurn(f)
	case !bytes.Contains(head, responseItem):
	case bytes.Contains(line, askTool):
		s.Filed, s.InTurn = true, true
	case bytes.Contains(line, roleAgent) || bytes.Contains(line, roleUser):
		var r codexRecord
		if json.Unmarshal(line, &r) != nil || r.Type != "response_item" || r.Payload.Type != "message" {
			return
		}
		var parts []string
		for _, c := range r.Payload.Content {
			if strings.TrimSpace(c.Text) != "" {
				parts = append(parts, c.Text)
			}
		}
		text := strings.Join(parts, "\n")
		switch r.Payload.Role {
		case "assistant":
			s.assistant(r.Timestamp, text, text != "", f)
		case "user":
			if ownerTyped(text) {
				s.human(r.Timestamp, f)
			} else {
				s.injected(r.Timestamp, f)
			}
		}
	default:
		if bytes.Contains(line, fileItem) {
			s.Filed, s.InTurn = true, true
		}
	}
}

func ownerTyped(text string) bool {
	t := strings.TrimSpace(text)
	if t == "" {
		return false
	}
	for _, p := range harness {
		if strings.HasPrefix(t, p) {
			return false
		}
	}
	return true
}
