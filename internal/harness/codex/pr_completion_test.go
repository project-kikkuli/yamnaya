package codex

import (
	"encoding/json"
	"testing"
)

func TestCompletedCommandsRecoverPullRequestsAndPushesAfterAnAsyncToolReturns(t *testing.T) {
	for _, tc := range []struct {
		name, command, output string
		code                  int
		prs, pushes           int
	}{
		{"created after yield", "gh pr create --head topic", "https://github.com/me/app/pull/6\n", 0, 1, 0},
		{"push refspec", "git -c credential.helper= push https://github.com/me/app.git HEAD:refs/heads/topic", "To https://github.com/me/app.git\n   a..b HEAD -> topic\n", 0, 0, 1},
		{"failed create", "gh pr create", "failed https://github.com/me/app/pull/6", 1, 0, 0},
		{"view is not ownership", "gh pr view 6", "https://github.com/me/app/pull/6", 0, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s scan
			for _, p := range []any{
				map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "c1", "input": tc.command},
				map[string]any{"type": "custom_tool_call_output", "call_id": "c1", "output": `{"session_id":80558,"output":""}`},
				map[string]any{"type": "item_completed", "item": map[string]any{"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", tc.command}, "aggregated_output": tc.output, "exit_code": tc.code}},
			} {
				data, _ := json.Marshal(p)
				kind := "response_item"
				if p.(map[string]any)["type"] == "item_completed" {
					kind = "event_msg"
				}
				s.add(rolloutLine{Type: kind, Payload: data}, "")
				data, _ = json.Marshal(s)
				if err := json.Unmarshal(data, &s); err != nil {
					t.Fatal(err)
				}
			}
			if len(s.Facts.PRs) != tc.prs || len(s.Facts.Pushes) != tc.pushes {
				t.Fatalf("PRs %v pushes %v", s.Facts.PRs, s.Facts.Pushes)
			}
		})
	}
}
