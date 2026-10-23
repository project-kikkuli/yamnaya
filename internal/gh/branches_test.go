package gh

import (
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestBranchDiscoveryExcludesSameNamedForkHeads(t *testing.T) {
	b := core.PRBranch{Repo: "me/app", Head: "topic"}
	s := &Source{Now: time.Now, Batch: func(q string, vars map[string]string) ([]byte, error) {
		if vars["o0"] != "me" || vars["r0"] != "app" || vars["h0"] != "topic" {
			t.Fatalf("vars %v", vars)
		}
		return []byte(`{"data":{"rateLimit":{"limit":5000,"remaining":4000,"resetAt":"2026-10-24T00:00:00Z"},"b0":{"pullRequests":{"nodes":[{"url":"https://github.com/me/app/pull/6","number":6,"headRepository":{"nameWithOwner":"me/app"}},{"url":"https://github.com/me/app/pull/7","number":7,"headRepository":{"nameWithOwner":"fork/app"}}]}}}}`), nil
	}}
	found, err := s.PRsForBranches([]core.PRBranch{b})
	if err != nil || len(found[b]) != 1 || found[b][0].Number != 6 {
		t.Fatalf("found %v: %v", found, err)
	}
	if left, _, ok := s.Budget(); !ok || left != 0.8 {
		t.Fatalf("budget %v known %v", left, ok)
	}
}
