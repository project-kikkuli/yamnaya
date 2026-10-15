package gh

import (
	"testing"
	"time"
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
