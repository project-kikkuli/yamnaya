package gh

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOnlyACheckRunNoNewerRunReplacedCountsAsFailed(t *testing.T) {
	url := "https://github.com/me/app/pull/1"
	overview := func(state string, failed int) string {
		return fmt.Sprintf(`{"data":{"viewer":{"login":"mira"},"p0":{"state":"OPEN","title":"T","headRefOid":"h1","mergeable":"MERGEABLE","mergeStateStatus":%q,"author":{"login":"mira"},
			"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"checkRunCountsByState":[{"state":"SUCCESS","count":4},{"state":"CANCELLED","count":%d}],"statusContextCountsByState":[]}}}}]}}}}`, state, failed)
	}
	run := func(id int, workflow, name, conclusion string) string {
		return fmt.Sprintf(`{"databaseId":%d,"name":%q,"conclusion":%q,"checkSuite":{"app":{"slug":"github-actions"},"workflowRun":{"workflow":{"name":%q}}}}`, id, name, conclusion, workflow)
	}
	page := func(next string, runs ...string) string {
		return fmt.Sprintf(`{"data":{"resource":{"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":%v,"endCursor":%q},"nodes":[%s]}}}}]}}}}`, next != "", next, strings.Join(runs, ","))
	}
	for _, c := range []struct {
		name       string
		state      string
		counted    int
		pages      []string
		err        error
		wantFailed int
		wantReads  int
	}{
		{"a cancelled run replaced by a pass", "CLEAN", 1, []string{page("", run(1, "w", "comment", "CANCELLED"), run(2, "w", "comment", "SUCCESS"))}, nil, 0, 1},
		{"a cancelled run replaced by a running one", "CLEAN", 1, []string{page("", run(1, "w", "comment", "CANCELLED"), run(2, "w", "comment", ""))}, nil, 0, 1},
		{"a failure with no newer run", "CLEAN", 1, []string{page("", run(1, "w", "test", "FAILURE"), run(2, "w", "lint", "SUCCESS"))}, nil, 1, 1},
		{"a pass replaced by a failure", "CLEAN", 1, []string{page("", run(1, "w", "test", "SUCCESS"), run(2, "w", "test", "FAILURE"))}, nil, 1, 1},
		{"a failure replaced by a failure", "CLEAN", 2, []string{page("", run(1, "w", "test", "FAILURE"), run(2, "w", "test", "FAILURE"))}, nil, 1, 1},
		{"the same job name in another workflow", "CLEAN", 1, []string{page("", run(1, "w", "test", "FAILURE"), run(2, "x", "test", "SUCCESS"))}, nil, 1, 1},
		{"the replacing run on a later page", "CLEAN", 1, []string{page("c1", run(1, "w", "comment", "CANCELLED")), page("", run(2, "w", "comment", "SUCCESS"))}, nil, 0, 2},
		{"merge state still being computed", "UNKNOWN", 1, []string{page("", run(1, "w", "comment", "CANCELLED"), run(2, "w", "comment", "SUCCESS"))}, nil, 0, 1},
		{"runs that cannot be read", "CLEAN", 1, nil, errors.New("gh: network down"), 1, 1},
		{"GitHub already says unstable", "UNSTABLE", 1, nil, nil, 1, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			var reads []string
			s := &Source{Now: time.Now, Batch: func(q string, vars map[string]string) ([]byte, error) {
				if !strings.Contains(q, "checkSuite") {
					return []byte(overview(c.state, c.counted)), nil
				}
				reads = append(reads, q)
				if vars["url"] != url {
					t.Fatalf("vars %v", vars)
				}
				if c.err != nil {
					return nil, c.err
				}
				return []byte(c.pages[len(reads)-1]), nil
			}}
			got, err := s.PRStatuses([]string{url})
			if err != nil {
				t.Fatal(err)
			}
			if got[url].Failed != c.wantFailed || len(reads) != c.wantReads {
				t.Fatalf("failed %d after %d run reads, want %d after %d", got[url].Failed, len(reads), c.wantFailed, c.wantReads)
			}
			if c.wantReads == 2 && !strings.Contains(reads[1], `after: "c1"`) {
				t.Fatalf("second read %q does not continue from the cursor", reads[1])
			}
		})
	}
}
