package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

type alpineLayout struct {
	sky                                 int
	focus, region                       string
	focusGroup                          expeditionGroup
	panorama                            []expeditionGroup
	places                              []string
	panelStart, panelWidth, panelHeight int
	lakeStart, lakeHeight               int
}

func (m Model) sceneFocus() string {
	if m.listMode() == modeWork || m.listMode() == modeReview {
		for _, p := range m.board.Projects {
			if p.Work != nil && p.Work.ID == m.workID {
				return "m:" + p.Key
			}
		}
	}
	if m.listMode() == modeAgent {
		return "a:" + m.sess + "/" + m.agent
	}
	if r, ok := m.selected(); ok {
		switch {
		case r.review != nil && r.project != nil:
			return "m:" + r.project.Key
		case r.need != nil:
			return "m:" + r.need.Project
		case r.member != nil:
			return "a:" + r.session.Key() + "/" + r.member.ID
		case r.session != nil:
			return "s:" + r.session.Key()
		case r.project != nil:
			return "m:" + r.project.Key
		case r.repo != nil:
			return "r:" + r.repo.Key
		}
	}
	switch m.listMode() {
	case modeSession:
		return "s:" + m.sess
	case modeDetail:
		return "m:" + m.detail
	case modeRepo:
		return "r:" + m.repo
	}
	return "*"
}

func (m Model) sceneLayout(top, room, used int) alpineLayout {
	l := alpineLayout{sky: m.skylineHeight(), focus: m.sceneFocus()}
	if l.sky == 0 {
		return l
	}
	if g := m.alpine.groups[l.focus]; g != nil {
		l.focusGroup, l.region = *g, g.region
	}
	l.panelStart, l.panelWidth = top, m.sceneWidth()
	if l.panelWidth > 0 {
		rows := len(strings.Split(ansi.Wrap(l.focusGroup.states(), l.panelWidth, " "), "\n"))
		summaryRows := len(strings.Split(ansi.Wrap(l.focusGroup.artifactSummary(), l.panelWidth, " "), "\n"))
		assessmentRows := 0
		if l.focusGroup.assessment != "" {
			assessmentRows = min(3, len(strings.Split(ansi.Wrap(l.focusGroup.assessment, l.panelWidth, " "), "\n"))) + 1
		}
		desired := assessmentRows + rows + 11 + summaryRows + min(2, l.focusGroup.artifactTotal())*5
		l.panelHeight = min(desired, max(0, room-top))
	}
	l.lakeStart = max(used+1, l.panelStart+l.panelHeight+1)
	if room-l.lakeStart >= 3 {
		l.lakeHeight = min(7, room-l.lakeStart)
	}
	alive := map[string]bool{}
	for _, r := range m.board.Repos {
		alive[r.Key] = true
	}
	var regions []string
	for _, region := range m.alpine.regions {
		if alive[region] {
			regions = append(regions, region)
		}
	}
	slots := max(1, m.width/28)
	visible := map[string]bool{}
	if len(regions) <= slots {
		for _, region := range regions {
			visible[region] = true
		}
	} else {
		for _, region := range regions[:slots-1] {
			visible[region] = true
		}
		if !visible[l.region] && alive[l.region] {
			delete(visible, regions[slots-2])
			visible[l.region] = true
		}
	}
	other := expeditionGroup{}
	omitted := 0
	for _, region := range regions {
		g := m.alpine.groups["r:"+region]
		if g == nil {
			continue
		}
		if visible[region] {
			l.panorama = append(l.panorama, *g)
			l.places = append(l.places, "r:"+region)
		} else {
			omitted++
			other.projects += g.projects
			other.prs += g.prs
			other.reviews += g.reviews
			other.preparing += g.preparing
			other.withAuthor += g.withAuthor
			other.needs += g.needs
			other.cut += g.cut
			for state := range g.counts {
				other.counts[state] += g.counts[state]
			}
			for state := range g.artifactCounts {
				other.artifactCounts[state] += g.artifactCounts[state]
			}
		}
	}
	if omitted > 0 {
		other.name = fmt.Sprintf("%d other repositories", omitted)
		l.panorama = append(l.panorama, other)
		l.places = append(l.places, "")
	}
	return l
}

func (g expeditionGroup) skylineState() string {
	state := fmt.Sprintf("%d agents · %d work", g.total(), g.counts[core.Busy])
	if g.needs > 0 {
		return state + fmt.Sprintf(" · %d need you", g.needs)
	}
	if g.counts[core.Waiting] > 0 {
		return state + fmt.Sprintf(" · %d wait", g.counts[core.Waiting])
	}
	return state
}
