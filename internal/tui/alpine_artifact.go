package tui

import (
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const (
	artifactUnknown = iota
	artifactBranch
	artifactCommits
	artifactOpen
	artifactCI
	artifactReview
	artifactFailed
	artifactApproved
	artifactMerged
	artifactClosed
	artifactStates
)

type alpineArtifact struct {
	key, label, project, region, team string
	scopes                            [4]string
	pr, known, working                bool
	commits                           int
	status                            core.PRStatus
}

func (a alpineArtifact) state() int {
	if !a.pr {
		if a.commits > 0 {
			return artifactCommits
		}
		return artifactBranch
	}
	if !a.known {
		return artifactUnknown
	}
	switch a.status.State {
	case "MERGED":
		return artifactMerged
	case "CLOSED":
		return artifactClosed
	}
	switch {
	case a.status.Failed > 0:
		return artifactFailed
	case a.status.Pending > 0:
		return artifactCI
	case a.status.Unresolved > 0 || a.status.Review == "CHANGES_REQUESTED":
		return artifactReview
	case a.status.Review == "APPROVED":
		return artifactApproved
	case a.status.Review == "REVIEW_REQUIRED":
		return artifactReview
	}
	return artifactOpen
}

func (a alpineArtifact) stateLabel() string {
	switch a.state() {
	case artifactBranch:
		return "branch"
	case artifactCommits:
		return "building"
	case artifactOpen:
		return "open"
	case artifactCI:
		return "CI running"
	case artifactReview:
		if a.status.Review == "CHANGES_REQUESTED" {
			return "changes requested"
		}
		if a.status.Unresolved > 0 {
			return plural(a.status.Unresolved, "thread") + " open"
		}
		return "review required"
	case artifactFailed:
		return "CI failed"
	case artifactApproved:
		return "approved"
	case artifactMerged:
		return "merged"
	case artifactClosed:
		return "closed unmerged"
	}
	return "status unknown"
}

func (a alpineArtifact) facts() string {
	if a.pr && !a.known {
		return "CI and review unknown"
	}
	parts := []string{plural(a.commits, "commit")}
	if a.pr && a.state() != artifactMerged && a.state() != artifactClosed {
		n := a.status.Passed + a.status.Failed + a.status.Pending
		if n > 0 {
			parts = append(parts, fmt.Sprintf("CI %d/%d", a.status.Passed, n))
		} else {
			parts = append(parts, "no CI checks")
		}
		if a.status.Unresolved > 0 {
			parts = append(parts, plural(a.status.Unresolved, "thread")+" open")
		}
	}
	return strings.Join(parts, " · ")
}

func (a alpineArtifact) ink() int {
	switch a.state() {
	case artifactFailed:
		return alpineFlare
	case artifactCI, artifactReview:
		return alpineGold
	case artifactMerged, artifactApproved:
		return alpineGlow
	case artifactClosed, artifactUnknown:
		return alpineCloud
	}
	return alpineIce
}

func (a alpineArtifact) rank() int {
	return [...]int{2, 6, 5, 4, 1, 3, 0, 7, 8, 9}[a.state()]
}
