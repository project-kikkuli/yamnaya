package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func captureMergeReadiness(c *core.Core) error {
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	all, err := c.Store.Work.List()
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, scope := range all {
		for _, source := range scope.Sources {
			if source.Kind != "github" || !source.LinkedPR() || seen[source.URL] {
				continue
			}
			seen[source.URL] = true
			st := statuses[source.URL]
			r := mergeReview(source.URL, st)
			current, err := c.Store.Work.Get(scope.ID)
			if err != nil {
				return err
			}
			if current.Reviews[r.ID].Version == r.Version {
				continue
			}
			if _, err = c.Store.Work.Commit(work.Event{Work: current.ID, Expected: current.Revision, Type: "merge-status", Actor: "GitHub", Review: r}); err != nil {
				return err
			}
		}
	}
	return nil
}

func mergeReview(url string, st core.PRStatus) work.Review {
	st.Fetched = time.Time{}
	raw, _ := json.Marshal(st)
	sum := sha256.Sum256(raw)
	r := work.Review{ID: work.MergeReviewID(url), URL: url, Kind: "merge", Action: "merge", Title: st.Title, Author: st.Merge.Author, Version: st.Merge.OID + ":" + hex.EncodeToString(sum[:8]), State: "waiting", Requester: "Tracked agent work", Question: "Merge this pull request?"}
	if r.Title == "" {
		r.Title = url
	}
	r.Evidence = fmt.Sprintf("Head %s\nCI: %d passed, %d failed, %d pending\nReview: %s\nOpen threads: %d\nMergeability: %s · %s", st.Merge.OID, st.Passed, st.Failed, st.Pending, st.Review, st.Unresolved, st.Merge.Mergeable, st.Merge.MergeState)
	switch {
	case st.State == "MERGED":
		r.State, r.Closed, r.Changed = work.Completed, true, "GitHub reports merged"
	case st.Closed():
		r.State, r.Closed, r.Changed = "withdrawn", true, "GitHub reports closed"
	case !st.OwnedPR():
		r.State, r.Closed, r.Changed = "withdrawn", true, "Automatic merge intake requires verified authorship by your GitHub account"
	case st.State == "UNKNOWN" || st.State == "" || st.Merge.OID == "" || st.Merge.Error != "":
		r.StatusError = st.Merge.Error
		if r.StatusError == "" {
			r.StatusError = "Merge readiness has not been verified"
		}
		r.Question, r.Recommendation = "Verify this PR's merge status", "Open GitHub to verify current readiness"
	case st.ReadyToMerge():
		r.State, r.Recommendation, r.Changed = work.Ready, "Merge this PR on GitHub", "Ready to merge: checks and review are satisfied, no open threads, clean merge state"
	default:
		r.Changed = "Agent work still has merge prerequisites to satisfy"
	}
	return r
}
