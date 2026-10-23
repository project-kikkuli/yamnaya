package gh

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestABatchReadsEveryPullRequestItCanAndLeavesTheRest(t *testing.T) {
	open := `{"state":"OPEN","title":"Retries","headRefName":"retries","headRefOid":"h1","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","author":{"login":"Mira"},
		"commits":{"totalCount":2},"reviewThreads":{"totalCount":1,"nodes":[{"isResolved":false}]},
		"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"checkRunCountsByState":[{"state":"SUCCESS","count":4},{"state":"FAILURE","count":1}],"statusContextCountsByState":[]}}}}]}}`
	urls := []string{"https://github.com/me/app/pull/1", "https://github.com/me/app/pull/2", "https://github.com/gone/app/pull/3"}
	for _, c := range []struct {
		name  string
		reply string
		err   error
		read  []string
	}{
		{"all read", `{"data":{"viewer":{"login":"mira"},"p0":` + open + `,"p1":{"state":"MERGED","author":{"login":"sol"}},"p2":` + open + `}}`, nil, urls},
		{"one missing beside an error", `{"data":{"viewer":{"login":"mira"},"p0":` + open + `,"p1":{"state":"MERGED","author":{"login":"sol"}},"p2":null},"errors":[{"type":"NOT_FOUND"}]}`, errors.New("exit status 1"), urls[:2]},
		{"rate limited", `{"errors":[{"type":"RATE_LIMITED"}]}`, errors.New("exit status 1"), nil},
		{"no reply", ``, errors.New("gh: network down"), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var queries []string
			s := &Source{Now: time.Now, Batch: func(q string, vars map[string]string) ([]byte, error) {
				queries = append(queries, q)
				if len(vars) != len(urls) || vars["u2"] != urls[2] {
					t.Fatalf("vars %v", vars)
				}
				return []byte(c.reply), c.err
			}}
			got, err := s.PRStatuses(urls)
			if len(queries) != 1 || !strings.Contains(queries[0], "p2: resource(url: $u2)") {
				t.Fatalf("queries %q", queries)
			}
			if (err != nil) != (c.read == nil) || len(got) != len(c.read) {
				t.Fatalf("read %v err %v, want %v", got, err, c.read)
			}
			for _, u := range c.read {
				if _, ok := got[u]; !ok {
					t.Fatalf("%s missing from %v", u, got)
				}
			}
			if c.read == nil {
				return
			}
			first := got[urls[0]]
			if first.State != "OPEN" || first.Merge.OID != "h1" || !first.OwnedPR() || first.Passed != 4 || first.Failed != 1 || first.Unresolved != 1 || first.Commits != 2 {
				t.Fatalf("first %+v", first)
			}
			if second := got[urls[1]]; second.State != "MERGED" || second.OwnedPR() {
				t.Fatalf("second %+v", second)
			}
		})
	}
}
