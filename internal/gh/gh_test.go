package gh

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const url = "https://github.com/me/app/pull/7"

func TestStatusFoldsChecksAndPrefersAFreshSharedCache(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "cache.json")
	if err := os.WriteFile(shared, []byte(`{"`+url+`":{"state":"OPEN","review":"APPROVED","checks":{"passed":5,"failed":0,"pending":1}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	modified := time.Now()
	if err := os.Chtimes(shared, modified, modified); err != nil {
		t.Fatal(err)
	}
	ran := 0
	view := `{"state":"OPEN","reviewDecision":"","title":"Retries","statusCheckRollup":[
		{"status":"COMPLETED","conclusion":"SUCCESS"},{"status":"COMPLETED","conclusion":"SKIPPED"},
		{"status":"COMPLETED","conclusion":"FAILURE"},{"status":"IN_PROGRESS","conclusion":""},{"state":"PENDING"},{"state":"ERROR"}]}`
	cases := []struct {
		name                    string
		age                     time.Duration
		passed, failed, pending int
		review                  string
		ranGH                   bool
	}{
		{"fresh shared cache", time.Minute, 5, 0, 1, "APPROVED", false},
		{"stale shared cache", 3 * time.Minute, 2, 2, 2, "", true},
	}
	for _, c := range cases {
		ran = 0
		s := &Source{Shared: shared, Now: func() time.Time { return modified.Add(c.age) },
			Run: func(string) ([]byte, error) { ran++; return []byte(view), nil }}
		st, err := s.PRStatus(url)
		if err != nil {
			t.Fatal(err)
		}
		if st.Passed != c.passed || st.Failed != c.failed || st.Pending != c.pending || st.Review != c.review || (ran > 0) != c.ranGH {
			t.Errorf("%s: status %+v, ran gh %d times", c.name, st, ran)
		}
	}
}
