package tui

import (
	"fmt"
	"math"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func (m Model) drawPanorama(c *alpineCanvas, l alpineLayout) {
	n := max(1, len(l.panorama))
	width := c.w / n
	for i := range n {
		g := expeditionGroup{region: "alpine"}
		place := ""
		if i < len(l.panorama) {
			g, place = l.panorama[i], l.places[i]
		}
		foreground := g
		if g.region == l.region && l.focusGroup.scale > 0 {
			foreground = l.focusGroup
		}
		tile := newAlpineCanvas(width, max(2, c.h-3))
		seed := alpineHash(g.region)
		for x := range tile.w {
			u := float64(x) / float64(max(1, tile.w-1))
			surface := float64(tile.h) - alpineProfile(u, seed)*float64(tile.h-1)
			for y := range tile.h {
				if float64(y+1) <= surface {
					continue
				}
				ink := alpineFar
				if float64(y)-surface < .5 {
					ink = alpineIce
				}
				tile.put(x, y, mountainEdge(surface, y), ink)
			}
		}
		cloud := int(m.alpine.frame/25) + seed%max(1, width)
		x := cloud%(width+14) - 10
		tile.label(x, 0, "  ⣀⣠⣤⣤⣀  ", alpineCloud)
		if tile.h >= 4 {
			tile.label(x-2, 1, "⠈⠉⠉⠉⠉⠉⠉⠁", alpineFar)
		}
		if foreground.counts[core.Busy] > 0 && tile.h >= 4 {
			step := int(m.alpine.frame/7) % 4
			xx, yy := tile.w/3+[]int{0, 1, 1, 0}[step], tile.h-1
			tile.put(xx, yy-1, 'o', alpineLamp)
			tile.put(xx, yy, []rune{'λ', 'ʎ'}[step%2], alpineMoss+1+m.alpine.hues[foreground.mission])
		}
		if len(foreground.artifacts) > 0 && tile.h >= 5 {
			mini := newAlpineCanvas(min(18, width/2), 5)
			m.drawArtifact(&mini, foreground.artifacts[0], place)
			for yy := 1; yy <= 3; yy++ {
				for xx := range mini.w {
					if r := mini.text[yy*mini.w+xx]; r != ' ' {
						tile.put(width-mini.w-1+xx, tile.h-4+yy, r, mini.ink[yy*mini.w+xx])
					}
				}
			}
		}
		if e, ok := m.alpine.effectAt(place); ok {
			tile.effect(e, tile.w/2, 1, m.alpine.frame)
		}
		for y := range tile.h {
			for x := range tile.w {
				c.put(i*width+x, y, tile.text[y*tile.w+x], tile.ink[y*tile.w+x])
			}
		}
		if i >= len(l.panorama) {
			continue
		}
		name, ink := g.name, alpineIce
		if g.prs > 0 {
			name += " · " + plural(g.prs, "PR")
		}
		if g.region == l.region && place != "" {
			name, ink = "› "+name, alpineGold
		}
		c.label(i*width, c.h-3, ansi.Truncate(name, width-2, "…"), ink)
		c.label(i*width, c.h-2, ansi.Truncate(g.skylineState(), width-2, "…"), alpineCloud)
		artifacts := fmt.Sprintf("%d PRs", g.prs)
		if len(foreground.artifacts) > 0 {
			a := foreground.artifacts[0]
			artifacts = a.label + " · " + a.stateLabel()
		}

		c.label(i*width, c.h-1, ansi.Truncate(artifacts, width-2, "…"), alpineCloud)
	}
}

func (m Model) drawRopeTeam(c *alpineCanvas, g expeditionGroup) {
	seed := alpineHash(g.region + g.mission)
	for x := range c.w {
		profile := alpineProfile(float64(x)/float64(max(1, c.w-1)), seed)
		surface := float64(c.h) - profile*float64(c.h-1)
		for y := range c.h {
			if float64(y+1) > surface {
				c.put(x, y, mountainEdge(surface, y), alpineFar)
			}
		}
	}
	working := g.counts[core.Busy]
	for i := range min(3, working) {
		step := (int(m.alpine.frame/6) + i) % 4
		x := 5 + i*7 + []int{0, 1, 1, 0}[step]
		y := c.h - 2 - i%2
		for xx := x - 5; xx < x-1; xx++ {
			c.put(xx, y, '·', alpineIce)
		}
		c.put(x, y-1, 'o', alpineLamp)
		c.put(x, y, []rune{'λ', 'ʎ'}[step%2], alpineMoss+1+m.alpine.hues[g.mission])
	}
	if g.counts[core.Waiting] > 0 {
		c.label(c.w-15, c.h-2, " /\\", alpineGold)
		c.label(c.w-15, c.h-1, "/△_\\", alpineGold)
	}
	if g.counts[core.Idle] > 0 {
		c.label(c.w-7, c.h-2, " /\\", alpineIce)
		c.label(c.w-7, c.h-1, "/___\\", alpineIce)
	}
	if g.counts[core.Done] > g.cut {
		c.put(c.w/2, 0, '⚑', alpineGlow)
	}
	if g.cut > 0 {
		c.label(c.w-3, c.h-1, "×", alpineCloud)
	}
	if g.counts[core.Unknown] > 0 {
		c.put(2, c.h-1, '?', alpineCloud)
	}
	if g.needs > 0 {
		for y := 1; y < min(4, c.h); y++ {
			c.put(c.w-3, y, '│', alpineFlare)
		}
		glyph := '✦'
		if m.alpine.frame%20 >= 10 {
			glyph = '·'
		}
		c.put(c.w-3, 0, glyph, alpineFlare)
	}
}

func (c *alpineCanvas) paste(x, y int, tile alpineCanvas) {
	for yy := range tile.h {
		for xx := range tile.w {
			c.put(x+xx, y+yy, tile.text[yy*tile.w+xx], tile.ink[yy*tile.w+xx])
		}
	}
}

func (c *alpineCanvas) caption(x, y int, text string, ink int) {
	c.label(x, y, ansi.Truncate(text, max(0, c.w-x), "…"), ink)
}

func (m Model) drawLake(c *alpineCanvas, l alpineLayout) {
	t := float64(m.alpine.frame) / 25
	for x := range c.w {
		c.put(x, 0, '⠤', alpineLake)
		for y := 1; y < c.h; y++ {
			n := max(1, len(l.panorama))
			span := max(1, c.w/n)
			index := min(n-1, x/span)
			region := "alpine"
			if index < len(l.panorama) {
				region = l.panorama[index].region
			}
			reflection := alpineProfile(float64(x-index*span)/float64(span), alpineHash(region))
			if y < int(reflection*float64(c.h)) && (x+y)%3 != 0 {
				c.put(x, y, '⠂', alpineFar)
			}
			wave := math.Sin(float64(x)*.17+t-float64(y)) + math.Sin(float64(x)*.08-t)
			if wave > 1.3 {
				c.put(x, y, '⠤', alpineLake)
			}
		}
	}
	for i, place := range l.places {
		e, ok := m.alpine.kindAt(place, "merge")
		if !ok {
			continue
		}
		age := float64(m.alpine.frame - e.frame)
		center := (i*2 + 1) * c.w / (max(1, len(l.places)) * 2)
		for y := range c.h {
			radius := int(age/3) + y*2
			c.put(center-radius, y, '⠤', alpineGlow)
			c.put(center+radius, y, '⠤', alpineGlow)
			if y%2 == 0 {
				c.put(center+int(math.Sin(age*.3+float64(y))*2), y, '✧', alpineGold)
			}
		}
	}
}
