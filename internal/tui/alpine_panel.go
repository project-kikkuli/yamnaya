package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

func (m Model) drawPanel(c *alpineCanvas, g expeditionGroup, place string) {
	c.caption(0, 0, "› "+g.name, alpineMoss+1+m.alpine.hues[g.mission])
	states := strings.Split(ansi.Wrap(g.states(), c.w, " "), "\n")
	for i, line := range states {
		c.caption(0, i+1, line, alpineIce)
	}
	start := len(states) + 2
	team := newAlpineCanvas(c.w, 6)
	m.drawRopeTeam(&team, g)
	if e, ok := m.alpine.effectAt(place); ok && e.artifact == "" {
		team.effect(e, c.w/2, 1, m.alpine.frame)
	}
	c.paste(0, start, team)
	start += team.h + 1
	summary := strings.Split(ansi.Wrap(g.artifactSummary(), c.w, " "), "\n")
	for i, line := range summary {
		c.caption(0, start+i, line, alpineCloud)
	}
	start += len(summary) + 1
	artifacts := slices.Clone(g.artifacts)
	if event, ok := m.alpine.effectAt(place); ok && event.artifact != "" {
		if a, found := m.alpine.artifacts[event.mountain+"\x00"+event.artifact]; found {
			artifacts = slices.DeleteFunc(artifacts, func(a alpineArtifact) bool { return a.key == event.artifact })
			artifacts = append([]alpineArtifact{a}, artifacts...)
		}
	}
	if g.artifactTotal() == 0 {
		c.caption(0, start, "no branch or PR linked", alpineCloud)
		return
	}
	shown := 0
	for _, a := range artifacts {
		if start+5 > c.h-1 {
			break
		}
		tile := newAlpineCanvas(c.w, 5)
		m.drawArtifact(&tile, a, place)
		c.paste(0, start, tile)
		start += 5
		shown++
	}
	if n := g.artifactTotal() - shown; n > 0 {
		c.caption(0, min(c.h-1, start), fmt.Sprintf("+%d other artifacts", n), alpineCloud)
	}
}

func (m Model) drawArtifact(c *alpineCanvas, a alpineArtifact, place string) {
	state := a.state()
	ink := a.ink()
	c.caption(0, 0, a.label+" · "+a.stateLabel(), ink)
	left := min(6, c.w/5)
	right := c.w - left - 1
	c.label(left, 1, "│", ink)
	c.label(right, 1, "│", ink)
	c.label(left, 3, "╱│", alpineRock)
	c.label(right-1, 3, "│╲", alpineRock)
	for x := left; x <= right; x++ {
		r := '┄'
		switch state {
		case artifactUnknown:
			r = '·'
		case artifactBranch:
			if x > left+2 {
				continue
			}
		case artifactCommits:
			if x > left+3+min(a.commits, 12) {
				continue
			}
			r = '═'
		case artifactClosed:
			if x > left+1 && x < right-1 {
				continue
			}
			r = '┊'
		case artifactMerged, artifactApproved:
			r = '═'
		case artifactFailed:
			if x > c.w/2-2 && x < c.w/2+2 {
				c.put(x, 3, '╲', ink)
				continue
			}
			r = '─'
		}
		c.put(x, 2, r, ink)
	}
	switch state {
	case artifactUnknown:
		c.put(c.w/2, 1, '?', alpineCloud)
	case artifactCI:
		x := left + 2 + int(m.alpine.frame/4)%max(1, right-left-3)
		c.put(x, 1, '◆', alpineGold)
	case artifactReview:
		c.put(c.w/2, 1, '?', alpineGold)
		if a.status.Unresolved > 0 || a.status.Review == "CHANGES_REQUESTED" {
			c.put(left+3, 1, '!', alpineFlare)
		}
	case artifactApproved:
		c.put(right-2, 1, '╳', alpineGold)
	case artifactMerged:
		c.put(right, 1, '⚑', alpineGold)
	case artifactCommits, artifactBranch, artifactOpen:
		if a.working {
			c.put(left+2, 1, 'o', alpineLamp)
			c.put(left+3, 1, []rune{'╱', '╲'}[m.alpine.frame/5%2], alpineIce)
		}
	}
	for _, kind := range []string{"merge", "fracture", "work"} {
		e, ok := m.alpine.kindAt(place, kind)
		if !ok || e.artifact != a.key {
			continue
		}
		effect := newAlpineCanvas(c.w, 3)
		effect.effect(e, c.w/2, 1, m.alpine.frame)
		for y := range effect.h {
			for x := range effect.w {
				if r := effect.text[y*effect.w+x]; r != ' ' {
					c.put(x, y+1, r, effect.ink[y*effect.w+x])
				}
			}
		}
	}
	c.caption(0, 4, a.facts(), alpineCloud)
}
