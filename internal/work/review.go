package work

import "time"

const (
	Preparing  = "preparing"
	Ready      = "ready"
	WithAuthor = "with author"
	Completed  = "completed"
)

type Review struct {
	FollowThrough   string
	FollowedBy      string
	ID              string
	Kind            string
	URL             string
	Title           string
	Requester       string
	Author          string
	Preparer        string
	Version         string
	Order           int64
	State           string
	Question        string
	Recommendation  string
	Evidence        string
	Changed         string
	Feedback        string
	Judgment        string
	PreparedVersion string
	PreparedContext int64
	Preparation     int64
	Provisional     bool
	Closed          bool
	Updated         time.Time
}

func (r Review) NextOwner(manager string) string {
	switch r.State {
	case Ready:
		return "owner"
	case WithAuthor:
		if r.Author != "" {
			return r.Author
		}
		if r.Requester != "" {
			return r.Requester
		}
		return "author"
	case Completed:
		if r.Judgment != "" && r.FollowThrough == "" {
			if manager != "" {
				return manager
			}
			return "unassigned"
		}
		return "none"
	case "incoming":
		return "unassigned"
	case Preparing:
		if manager != "" {
			return manager
		}
		return "unassigned manager"
	default:
		return "none"
	}
}
