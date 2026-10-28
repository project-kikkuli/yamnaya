package core

import "strings"

type PRMergeFacts struct {
	Author     string
	Viewer     string
	OID        string
	Draft      bool
	Mergeable  string
	MergeState string
	Error      string
}

// ReadyToMerge reports whether GitHub shows the pull request open, green, not a draft, approved or unreviewed, with no open threads and a clean merge.
// A clean merge state is GitHub's word that the checks pass, so a run a newer one of the same check superseded does not hold it back.
func (s PRStatus) ReadyToMerge() bool {
	return s.State == "OPEN" && s.Merge.OID != "" && !s.Merge.Draft && s.Merge.Mergeable == "MERGEABLE" &&
		s.Merge.MergeState == "CLEAN" && s.Pending == 0 && s.Unresolved == 0 &&
		(s.Review == "" || s.Review == "APPROVED") && s.Merge.Error == ""
}

// OwnedPR reports whether the owner's GitHub account wrote the pull request.
func (s PRStatus) OwnedPR() bool {
	return s.Merge.Author != "" && s.Merge.Viewer != "" && strings.EqualFold(s.Merge.Author, s.Merge.Viewer)
}

// settled is st with prev's merge state while GitHub, recomputing it lazily after the base moved, answers UNKNOWN for the same head.
func (st PRStatus) settled(prev PRStatus) PRStatus {
	if st.Merge.MergeState == "UNKNOWN" && st.Merge.OID != "" && st.Merge.OID == prev.Merge.OID && prev.Merge.MergeState != "" && prev.Merge.MergeState != "UNKNOWN" {
		st.Merge.Mergeable, st.Merge.MergeState = prev.Merge.Mergeable, prev.Merge.MergeState
	}
	return st
}
