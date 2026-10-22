package gh

import (
	"errors"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const url = "https://github.com/me/app/pull/7"

func TestStatusFoldsChecksAndCountsUnresolvedThreads(t *testing.T) {
	cases := []struct {
		name                    string
		body                    string
		passed, failed, pending int
		threads, unresolved     int
		head                    string
	}{
		{"checks and threads", `{"data":{"resource":{"state":"OPEN","title":"Retries","headRefName":"retries","commits":{"totalCount":3},
			"reviewThreads":{"totalCount":3,"nodes":[{"isResolved":true},{"isResolved":false},{"isResolved":true}]},
			"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{
			"checkRunCountsByState":[{"state":"SUCCESS","count":70},{"state":"SKIPPED","count":45},{"state":"FAILURE","count":1},
			{"state":"IN_PROGRESS","count":3},{"state":"QUEUED","count":0}],
			"statusContextCountsByState":[{"state":"PENDING","count":1},{"state":"ERROR","count":1},{"state":"SUCCESS","count":0}]}}}}]}}}}`,
			115, 2, 4, 3, 1, "retries"},
		{"no checks, threads past the first page", `{"data":{"resource":{"state":"MERGED","headRefName":"x","commits":{"totalCount":1},
			"reviewThreads":{"totalCount":102,"nodes":[` + resolved(100) + `]},"last":{"nodes":[{"commit":{"statusCheckRollup":null}}]}}}}`,
			0, 0, 0, 102, 2, "x"},
	}
	for _, c := range cases {
		s := &Source{Now: time.Now, Run: func(string) ([]byte, error) { return []byte(c.body), nil }}
		st, err := s.PRStatus(url)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if st.Passed != c.passed || st.Failed != c.failed || st.Pending != c.pending || st.Threads != c.threads || st.Unresolved != c.unresolved || st.Head != c.head {
			t.Errorf("%s: status %+v", c.name, st)
		}
	}
}

func resolved(n int) string {
	out := ""
	for i := range n {
		if i > 0 {
			out += ","
		}
		out += `{"isResolved":true}`
	}
	return out
}

func TestAMissingPullRequestIsGoneOnlyWhenItsRepoIsMissing(t *testing.T) {
	for _, c := range []struct {
		name      string
		exists    bool
		lookup    error
		wantState string
		wantErr   bool
	}{
		{"no such repo", false, nil, core.PRGone, false},
		{"repo exists, pull request unreadable", true, nil, "", true},
		{"repo lookup failed", false, errors.New("gh: HTTP 502"), "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var asked string
			s := &Source{Now: time.Now,
				Run:  func(string) ([]byte, error) { return []byte(`{"data":{"resource":null}}`), nil },
				Repo: func(repo string) (bool, error) { asked = repo; return c.exists, c.lookup }}
			st, err := s.PRStatus(url)
			if st.State != c.wantState || (err != nil) != c.wantErr || asked != "me/app" {
				t.Fatalf("status %+v, err %v, asked about %q", st, err, asked)
			}
		})
	}
}

func TestGitHubMergeFactsAreReadWithTheHeadAndThreadEvidence(t *testing.T) {
	s := &Source{Now: time.Now, Run: func(string) ([]byte, error) {
		return []byte(`{"data":{"resource":{"state":"OPEN","title":"Caller migration","headRefOid":"head1","isDraft":false,"mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","reviewDecision":"APPROVED","reviewThreads":{"totalCount":0,"nodes":[]},"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"checkRunCountsByState":[{"state":"SUCCESS","count":8}]}}}}]}}}}`), nil
	}}
	st, err := s.PRStatus(url)
	if err != nil || st.Merge != (core.PRMergeFacts{OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}) || st.Passed != 8 {
		t.Fatalf("merge facts were lost: %+v %v", st, err)
	}
}
