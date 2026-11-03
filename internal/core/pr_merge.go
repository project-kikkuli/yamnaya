package core

import (
	"strings"
	"time"
)

// prMergeFresh is how long a read can vouch for a merge: past the slowest pace, reads have stopped and the pull request may have moved.
const prMergeFresh = 2 * PRSlowest

type PRMergeFacts struct {
	Author     string
	Viewer     string
	OID        string
	Draft      bool
	Mergeable  string
	MergeState string
	Error      string
}

// ReadyToMerge reports whether a recent read shows the pull request open, green, not a draft, approved or unreviewed, with no open threads and a clean merge.
func (s PRStatus) ReadyToMerge(now time.Time) bool {
	return now.Sub(s.Fetched) < prMergeFresh && s.State == "OPEN" && s.Merge.OID != "" && !s.Merge.Draft && s.Merge.Mergeable == "MERGEABLE" &&
		s.Merge.MergeState == "CLEAN" && s.Failed == 0 && s.Pending == 0 && s.Unresolved == 0 &&
		(s.Review == "" || s.Review == "APPROVED") && s.Merge.Error == ""
}

// ReadyForOwner reports whether the pull request belongs in the owner's merge queue: ready to merge and approved.
func (s PRStatus) ReadyForOwner(now time.Time) bool {
	return s.ReadyToMerge(now) && s.Review == "APPROVED"
}

// OwnedPR reports whether the owner's GitHub account wrote the pull request.
func (s PRStatus) OwnedPR() bool {
	return s.Merge.Author != "" && s.Merge.Viewer != "" && strings.EqualFold(s.Merge.Author, s.Merge.Viewer)
}

// Foreign reports whether GitHub shows someone other than the owner's account wrote the pull request.
func (s PRStatus) Foreign() bool {
	return s.Merge.Author != "" && s.Merge.Viewer != "" && !strings.EqualFold(s.Merge.Author, s.Merge.Viewer)
}

// settled is st with prev's merge state while GitHub, recomputing it lazily after the base moved, answers UNKNOWN for the same head.
func (st PRStatus) settled(prev PRStatus) PRStatus {
	if st.Merge.MergeState == "UNKNOWN" && st.Merge.OID != "" && st.Merge.OID == prev.Merge.OID && prev.Merge.MergeState != "" && prev.Merge.MergeState != "UNKNOWN" {
		st.Merge.Mergeable, st.Merge.MergeState = prev.Merge.Mergeable, prev.Merge.MergeState
	}
	return st
}
