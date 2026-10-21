package core

type PRMergeFacts struct {
	OID        string
	Draft      bool
	Mergeable  string
	MergeState string
	Error      string
}

func (s PRStatus) ReadyToMerge() bool {
	return s.State == "OPEN" && s.Merge.OID != "" && !s.Merge.Draft && s.Merge.Mergeable == "MERGEABLE" &&
		s.Merge.MergeState == "CLEAN" && s.Failed == 0 && s.Pending == 0 && s.Unresolved == 0 &&
		(s.Review == "" || s.Review == "APPROVED") && s.Merge.Error == ""
}
