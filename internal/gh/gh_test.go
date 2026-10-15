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
			"last":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
			{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"COMPLETED","conclusion":"SKIPPED"},
			{"status":"COMPLETED","conclusion":"FAILURE"},{"status":"IN_PROGRESS","conclusion":""},{"state":"PENDING"},{"state":"ERROR"}]}}}}]}}}}`,
			2, 2, 2, 3, 1, "retries"},
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
