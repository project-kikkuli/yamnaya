package usage

import (
	"testing"
	"time"
)

func TestOnlyNativeStructuredErrorsProduceExhaustionEvents(t *testing.T) {
	for _, tc := range []struct {
		h, line string
		want    bool
	}{
		{"codex", `{"type":"event_msg","timestamp":"2026-10-23T12:00:00Z","payload":{"type":"error","codex_error_info":"UsageLimitExceeded"}}`, true},
		{"codex", `{"type":"event_msg","payload":{"type":"error","codex_error_info":"RateLimitExceeded"}}`, false},
		{"codex", `{"type":"event_msg","payload":{"type":"error","codex_error_info":"ServerOverloaded","message":"UsageLimitExceeded"}}`, false},
		{"codex", `{"type":"response_item","payload":{"type":"function_call_output","output":"usage_limit_exceeded"}}`, false},
		{"claude", `{"type":"assistant","timestamp":"2026-10-23T12:00:00Z","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>"}}`, true},
		{"claude", `{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"usageLimitDenial rate_limit"}]}}`, false},
		{"claude", `{"type":"assistant","isApiErrorMessage":true,"error":"overloaded"}`, false},
	} {
		e := exhaustionEvent([]byte(tc.line), tc.h, "model")
		if (e != nil) != tc.want {
			t.Fatalf("%s: %s -> %#v", tc.h, tc.line, e)
		}
		if e != nil && e.Seen.Equal(time.Time{}) {
			t.Fatal("native observation missing timestamp")
		}
	}
}
