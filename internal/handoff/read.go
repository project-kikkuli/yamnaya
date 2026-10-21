package handoff

import (
	"bufio"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func Read(file File, offset int64) ([]Event, int64, error) {
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if info.Size() < offset {
		offset = 0
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	reader := bufio.NewReader(f)
	var out []Event
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF {
			return out, offset, nil
		}
		if err != nil {
			return out, offset, err
		}
		start := offset
		offset += int64(len(line))
		texts, id, at, decodeErr := decode(file.Harness, line)
		if decodeErr != nil {
			out = append(out, Event{ID: Key(file.Path, string(line)), Work: file.Work, Harness: file.Harness, Session: file.Session, Agent: file.Agent, Path: file.Path, Offset: start, Error: fmt.Sprintf("Unreadable transcript record at byte %d: %v", start, decodeErr)})
			continue
		}
		for i, text := range texts {
			agent := file.Agent
			if text.agent != "" {
				agent = text.agent
			}
			identity := id
			if identity == "" {
				identity = Key(string(line))
			}
			out = append(out, Event{ID: Key(file.Harness, file.Session, identity, fmt.Sprint(i)), Work: file.Work, Harness: file.Harness, Session: file.Session, Agent: agent, Path: file.Path, Offset: start, At: at, Text: text.text})
		}
	}
}

type messageText struct{ text, agent string }

func decode(harness string, raw []byte) ([]messageText, string, time.Time, error) {
	var r struct {
		Type, UUID string
		Timestamp  time.Time
		Message    struct {
			Role    string
			Content json.RawMessage
			Stop    string `json:"stop_reason"`
		}
		Payload struct {
			Type, Role, Phase string
			Content           json.RawMessage
			Last              string `json:"last_agent_message"`
		}
		Meta    bool `json:"isMeta"`
		Summary bool `json:"isCompactSummary"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, "", r.Timestamp, err
	}
	var text string
	switch harness {
	case "claude":
		if r.Summary || r.Meta {
			return nil, r.UUID, r.Timestamp, nil
		}
		if r.Type == "assistant" {
			text = blocks(r.Message.Content, "text")
		}
		if r.Type == "user" {
			input := blocks(r.Message.Content, "text")
			if start := strings.Index(input, "<teammate-message"); start >= 0 {
				dec := xml.NewDecoder(strings.NewReader(input[start:]))
				token, err := dec.Token()
				if err == nil {
					if tag, ok := token.(xml.StartElement); ok {
						agent := ""
						for _, a := range tag.Attr {
							if a.Name.Local == "teammate_id" {
								agent = a.Value
							}
						}
						content := input[start:]
						open := strings.Index(content, ">")
						end := strings.LastIndex(content, "</teammate-message>")
						if agent != "" && open >= 0 && end > open {
							return []messageText{{text: strings.TrimSpace(content[open+1 : end]), agent: agent}}, r.UUID, r.Timestamp, nil
						}
					}
				}
			}
		}
	case "codex":
		if r.Type == "response_item" && r.Payload.Type == "message" && r.Payload.Role == "assistant" {
			text = blocks(r.Payload.Content, "output_text")
		}
	default:
		return nil, "", r.Timestamp, fmt.Errorf("unsupported transcript harness %s", harness)
	}
	if strings.TrimSpace(text) == "" {
		return nil, r.UUID, r.Timestamp, nil
	}
	return []messageText{{text: text}}, r.UUID, r.Timestamp, nil
}

func blocks(raw json.RawMessage, kind string) string {
	var plain string
	if json.Unmarshal(raw, &plain) == nil {
		return plain
	}
	var blocks []struct{ Type, Text string }
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var parts []string
	for _, b := range blocks {
		if b.Type == kind {
			parts = append(parts, b.Text)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}
