package gh

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestLandingReadsMergeCommitCIAndOpenThreads(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want core.Landing
	}{
		{"merged with mixed checks", `{"data":{"resource":{"state":"MERGED","mergeCommit":{"oid":"abc","statusCheckRollup":{"contexts":{
			"checkRunCountsByState":[{"state":"SUCCESS","count":5},{"state":"FAILURE","count":1},{"state":"IN_PROGRESS","count":2}],
			"statusContextCountsByState":[{"state":"SUCCESS","count":1}]}}},"reviewThreads":{"totalCount":2,"nodes":[{"isResolved":true},{"isResolved":false}]}}}}`,
			core.Landing{State: "MERGED", MergeCommit: "abc", Passed: 6, Failed: 1, Pending: 2, Unresolved: 1}},
		{"merged with no checks", `{"data":{"resource":{"state":"MERGED","mergeCommit":{"oid":"def","statusCheckRollup":null},"reviewThreads":{"totalCount":0,"nodes":[]}}}}`,
			core.Landing{State: "MERGED", MergeCommit: "def"}},
		{"merged, no checks yet after a parent that ran them", `{"data":{"resource":{"state":"MERGED","mergeCommit":{"oid":"fed","statusCheckRollup":null,
			"parents":{"nodes":[{"statusCheckRollup":{"state":"SUCCESS"}}]}},"reviewThreads":{"totalCount":0,"nodes":[]}}}}`,
			core.Landing{State: "MERGED", MergeCommit: "fed", Pending: 1}},
		{"merged, no checks after a parent that ran none", `{"data":{"resource":{"state":"MERGED","mergeCommit":{"oid":"cab","statusCheckRollup":null,
			"parents":{"nodes":[{"statusCheckRollup":null}]}},"reviewThreads":{"totalCount":0,"nodes":[]}}}}`,
			core.Landing{State: "MERGED", MergeCommit: "cab"}},
		{"open, threads past the first page", `{"data":{"resource":{"state":"OPEN","mergeCommit":null,"reviewThreads":{"totalCount":101,"nodes":[` + resolved(100) + `]}}}}`,
			core.Landing{State: "OPEN", Unresolved: 1}},
		{"someone else's", `{"data":{"viewer":{"login":"rajatscode"},"resource":{"state":"OPEN","author":{"login":"rtolsma"},"mergeCommit":null,"reviewThreads":{"totalCount":0,"nodes":[]}}}}`,
			core.Landing{State: "OPEN", Author: "rtolsma", Viewer: "rajatscode"}},
		{"no such pull request", `{"data":{"resource":null}}`, core.Landing{State: core.PRGone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Source{LandingRun: func(string) ([]byte, error) { return []byte(tc.body), nil }}
			got, err := s.Landing(url)
			if err != nil || got != tc.want {
				t.Fatalf("landing %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}
