package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type expeditionGroup struct {
	region, mission, name          string
	projects                       int
	artifacts                      []alpineArtifact
	artifactCounts                 [artifactStates]int
	prs                            int
	reviews, preparing, withAuthor int
	assessment                     string
	scale                          int
	cut                            int
	needs                          int
	counts                         [6]int
	sample                         []expedition
}

func (g expeditionGroup) total() int {
	n := 0
	for _, count := range g.counts {
		n += count
	}
	return n
}

func (g *expeditionGroup) add(e expedition) {
	g.counts[e.status]++
	if e.cut {
		g.cut++
	}
	if len(g.sample) < 6 {
		g.sample = append(g.sample, e)
	}
}

func expeditionRank(e expedition) int {
	if e.cut {
		return 5
	}
	return map[core.Status]int{core.Busy: 0, core.Waiting: 1, core.Idle: 2, core.Unknown: 3, core.Done: 4}[e.status]
}

func (g *expeditionGroup) addArtifact(a alpineArtifact) {
	g.artifactCounts[a.state()]++
	if a.pr {
		g.prs++
	}
	g.artifacts = append(g.artifacts, a)
	slices.SortFunc(g.artifacts, func(a, b alpineArtifact) int {
		if rank := a.rank() - b.rank(); rank != 0 {
			return rank
		}
		return strings.Compare(a.key, b.key)
	})
	if len(g.artifacts) > 3 {
		g.artifacts = g.artifacts[:3]
	}
}

func (g expeditionGroup) artifactTotal() int {
	n := 0
	for _, count := range g.artifactCounts {
		n += count
	}
	return n
}

func (g expeditionGroup) states() string {
	var parts []string
	for _, s := range []struct {
		count int
		name  string
	}{
		{g.counts[core.Busy], "working"}, {g.counts[core.Waiting], "waiting"}, {g.counts[core.Idle], "idle"},
		{max(0, g.counts[core.Done]-g.cut), "done"}, {g.cut, "interrupted"}, {g.counts[core.Unknown], "unknown"}, {g.counts[core.Moved], "moved"},
	} {
		if s.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", s.count, s.name))
		}
	}
	if g.needs > 0 {
		parts = append(parts, fmt.Sprintf("%d need you", g.needs))
	}
	if len(parts) == 0 {
		return "no live agents"
	}
	return strings.Join(parts, " · ")
}

func (g expeditionGroup) artifactSummary() string {
	parts := []string{plural(g.prs, "PR")}
	if g.reviews > 0 {
		parts = append(parts, fmt.Sprintf("%d judgments ready", g.reviews))
	}
	if g.preparing > 0 {
		parts = append(parts, fmt.Sprintf("%d preparing", g.preparing))
	}
	if g.withAuthor > 0 {
		parts = append(parts, fmt.Sprintf("%d with authors", g.withAuthor))
	}
	for _, state := range []struct {
		count int
		name  string
	}{
		{g.artifactCounts[artifactBranch] + g.artifactCounts[artifactCommits], "branches"},
		{g.artifactCounts[artifactFailed], "CI failed"},
		{g.artifactCounts[artifactCI], "CI running"},
		{g.artifactCounts[artifactReview], "in review"},
		{g.artifactCounts[artifactApproved], "approved"},
		{g.artifactCounts[artifactMerged], "merged"},
		{g.artifactCounts[artifactClosed], "closed"},
		{g.artifactCounts[artifactUnknown], "unknown"},
	} {
		if state.count > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", state.count, state.name))
		}
	}
	return strings.Join(parts, " · ")
}
