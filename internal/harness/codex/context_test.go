package codex

import (
	"encoding/json"
	"testing"
	"time"
)

func TestContextIsWhatTheLatestTokenCountSaysTheTurnRead(t *testing.T) {
	at := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	count := func(input int64) rolloutLine {
		raw, _ := json.Marshal(map[string]any{"type": "token_count", "info": map[string]any{
			"total_token_usage": map[string]int64{"input_tokens": 999_999},
			"last_token_usage":  map[string]int64{"input_tokens": input, "cached_input_tokens": input / 2}}})
		return rolloutLine{Type: "event_msg", Timestamp: at, Payload: raw}
	}
	empty, _ := json.Marshal(map[string]any{"type": "token_count", "info": nil})
	for _, tc := range []struct {
		name  string
		lines []rolloutLine
		want  int64
	}{
		{"one count", []rolloutLine{count(52_321)}, 52_321},
		{"the latest count wins", []rolloutLine{count(52_321), count(30_068)}, 30_068},
		{"a count without usage leaves it", []rolloutLine{count(52_321), {Type: "event_msg", Timestamp: at, Payload: empty}}, 52_321},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s scan
			for _, l := range tc.lines {
				s.add(l, "/repo")
			}
			if s.Facts.Context != tc.want {
				t.Fatalf("context = %d, want %d", s.Facts.Context, tc.want)
			}
		})
	}
}
