package claude

import (
	"encoding/json"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestSuccessfulBashPushDestinationsSurviveAColdScan(t *testing.T) {
	output, _ := json.Marshal("To https://github.com/me/app.git\n   a..b HEAD -> topic\n")
	w := newWorld(t,
		toolUse("push", "Bash", `{"command":"git push origin HEAD:topic"}`),
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"push","content":`+string(output)+`}]}}`,
	)
	cache := memCache{}
	w.session(t, cache)
	s := w.session(t, cache)
	if len(s.Pushes) != 1 || s.Pushes[0] != (core.PRBranch{Repo: "me/app", Head: "topic"}) {
		t.Fatalf("push destinations %v", s.Pushes)
	}
}
