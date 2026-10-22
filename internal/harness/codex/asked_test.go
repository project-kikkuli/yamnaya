package codex

import (
	"encoding/json"
	"testing"
	"time"
)

func TestTheLatestAskingReplyStaysUntilThePersonWritesAgain(t *testing.T) {
	at := time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)
	user := func(text string) rolloutLine {
		raw, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": []map[string]string{{"type": "input_text", "text": text}}})
		return rolloutLine{Type: "response_item", Timestamp: at, Payload: raw}
	}
	reply := func(text string, minute int) rolloutLine {
		raw, _ := json.Marshal(map[string]any{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": text}}})
		return rolloutLine{Type: "response_item", Timestamp: at.Add(time.Duration(minute) * time.Minute), Payload: raw}
	}
	const ask = "Open for you:\n4. Keep the login or switch?"
	for _, tc := range []struct {
		name  string
		lines []rolloutLine
		asked string
	}{
		{"asking reply", []rolloutLine{user("check"), reply(ask, 1)}, ask},
		{"follow-up keeps the block", []rolloutLine{user("check"), reply(ask, 1), reply("Next: answer item 4.", 2)}, ask + "\n\nNext: answer item 4."},
		{"the person wrote again", []rolloutLine{user("check"), reply(ask, 1), user("keep"), reply("Kept it.", 3)}, ""},
		{"nothing asked", []rolloutLine{user("check"), reply("All green.", 1)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s scan
			for _, l := range tc.lines {
				s.add(l, "/repo")
			}
			last := tc.lines[len(tc.lines)-1]
			if s.Facts.Asked != tc.asked || !s.Facts.ResultAt.Equal(last.Timestamp) {
				t.Fatalf("asked %q at %v, want %q at %v", s.Facts.Asked, s.Facts.ResultAt, tc.asked, last.Timestamp)
			}
		})
	}
}
