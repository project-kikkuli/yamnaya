package gh

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// TestAPullRequestGitHubCannotFindIsReadHourlyAndSaysSo runs seventy minutes of ym serve's five-second cycle over a session
// whose pull request GitHub cannot read: a 404 is recorded on the pull request and asked again hourly, any other failure every cycle it is due.
func TestAPullRequestGitHubCannotFindIsReadHourlyAndSaysSo(t *testing.T) {
	for _, c := range []struct {
		name, answer, state, text string
		least, most               int
	}{
		{"repository deleted", "gh: Not Found (HTTP 404)", core.PRNotFound, "pull request not found on GitHub", 2, 2},
		{"GitHub erroring", "gh: Bad Gateway (HTTP 502)", "UNKNOWN", "UNKNOWN", 30, 840},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
			const url = "https://github.com/rajat/gone/pull/15"
			reads := 0
			src := &Source{Now: func() time.Time { return now },
				Batch: func(string, map[string]string) ([]byte, error) {
					return []byte(`{"data":{"viewer":{"login":"mira"},"rateLimit":{"limit":5000,"remaining":5000,"resetAt":"2030-01-01T00:00:00Z"},"p0":null}}`), nil
				},
				REST: func(endpoint string) ([]byte, error) {
					if endpoint == "user" {
						return []byte(`{"login":"mira"}`), nil
					}
					reads++
					return nil, errors.New("GitHub " + endpoint + ": exit status 1: " + c.answer)
				}}
			cc := &core.Core{Store: st, PRs: src, Now: func() time.Time { return now }}
			b := core.Board{Sessions: []core.Live{{Session: core.Session{ID: "33d8d74c", Facts: core.Facts{PRs: []core.PR{{URL: url}}}}}}}
			for range int(70 * time.Minute / (5 * time.Second)) {
				cc.RefreshPRs(b)
				now = now.Add(5 * time.Second)
			}
			known, err := st.PRStatuses()
			if err != nil {
				t.Fatal(err)
			}
			got := known[url]
			if got.State != c.state || got.String() != c.text {
				t.Fatalf("recorded %q, shown %q; want %q shown %q", got.State, got.String(), c.state, c.text)
			}
			if reads < c.least || reads > c.most {
				t.Fatalf("%d reads of %s in seventy minutes, want %d to %d", reads, url, c.least, c.most)
			}
		})
	}
}
