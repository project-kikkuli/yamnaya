package work

import "strings"

type Source struct {
	Origin         string
	Kind           string
	URL            string
	Requester      string
	Question       string
	Coverage       string
	RequestVersion string
}

func (s Source) AutomaticRequest() bool {
	return s.Origin == "github-request" || (s.LinkedPR() && s.RequestVersion != "") ||
		(s.Origin == "" && s.Kind == "github" && s.Coverage == "explicit GitHub review-request assignment" && strings.HasPrefix(s.Requester, "GitHub review assignment to "))
}

func (s Source) LinkedPR() bool {
	return s.Origin == "session-pr" || (s.Origin == "" && s.Coverage == "linked agent PR; human review request not yet confirmed")
}
