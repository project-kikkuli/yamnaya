package cli

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestOwnerPullRequestsReachNeedsYouOnEveryHost(t *testing.T) {
	const merge, review = "https://github.com/acme/app/pull/19310", "https://github.com/acme/app/pull/77"
	gh := intake.Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		last := args[len(args)-1]
		switch {
		case slices.Contains(args, "--slurp"):
			return nil, errors.New("unknown flag: --slurp")
		case last == "user" || args[1] == "user":
			return []byte("owner\n"), nil
		case strings.HasSuffix(last, "/requested_reviewers"):
			return []byte(`{"users":[{"login":"owner"}],"teams":[]}`), nil
		case strings.Contains(last, "author%3Aowner"):
			return []byte(`{"total_count":1,"items":[{"title":"Ship it","user":{"login":"owner"},"pull_request":{"html_url":"` + merge + `"}}]}`), nil
		}
		return []byte(`{"total_count":1,"items":[{"updated_at":"2026-11-03T00:00:00Z","title":"Read CSV","user":{"login":"mira"},"pull_request":{"html_url":"` + review + `"}}]}`), nil
	}}
	for _, tc := range []struct {
		name    string
		carried bool
	}{{"owner's own host", false}, {"host another host carries", true}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			if tc.carried {
				if _, err := c.ServeSync(core.RelaySync{As: "vm", From: "laptop"}); err != nil {
					t.Fatal(err)
				}
			}
			green := core.PRStatus{State: "OPEN", Review: "APPROVED", Passed: 95, Fetched: time.Now(),
				Merge: core.PRMergeFacts{Author: "owner", Viewer: "owner", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
			if err := st.SavePRStatus(merge, green); err != nil {
				t.Fatal(err)
			}
			poll := reviewPoll{fetch: gh.Requests, authored: gh.Authored, every: time.Minute}
			if err := poll.poll(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			b, err := c.Board()
			if err != nil {
				t.Fatal(err)
			}
			for what, is := range map[string]func(core.Need) bool{
				"approved green pull request to merge": func(n core.Need) bool { return n.PR != nil && n.PR.URL == merge },
				"review request":                       func(n core.Need) bool { return n.Review != nil && n.Review.URL == review },
			} {
				if !slices.ContainsFunc(b.Needs, is) {
					t.Errorf("%s missing from Needs You: %+v", what, b.Needs)
				}
			}
		})
	}
}
