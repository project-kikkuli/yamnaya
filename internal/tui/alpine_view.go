package tui

import (
	"fmt"
	"hash/fnv"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func alpineHash(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	seed := h.Sum32()
	seed ^= seed >> 16
	seed *= 0x85ebca6b
	seed ^= seed >> 13
	seed *= 0xc2b2ae35
	return int(seed ^ (seed >> 16))
}

const sceneGap = 4

func (m Model) sceneWidth() int {
	if m.alpine.hidden || m.width < 148 || m.height < 18 {
		return 0
	}
	switch m.mode {
	case modeHelp, modeScreen, modeWhy, modePick, modeItems:
		return 0
	case modeInput:
		if m.inKind == inNewRepo {
			return 0
		}
	}
	return 44
}

func (m Model) paintView(page viewPage) string {
	width := m.sceneWidth()
	if width == 0 {
		return page.String()
	}
	var b strings.Builder
	var stage alpineCanvas
	for y, line := range page.lines {
		if y > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line.text)
		g := m.alpine.groups[line.place]
		if !line.scene || g == nil {
			continue
		}
		if y == 0 || !page.lines[y-1].scene || page.lines[y-1].place != line.place || line.world == 0 {
			stage = newAlpineCanvas(width, line.extent)
			m.drawScene(&stage, *g, line.place)
		}
		b.WriteString(strings.Repeat(" ", m.width-width-ansi.StringWidth(line.text)))
		b.WriteString(stage.row(line.world, 0, width))
	}
	return b.String()
}

func (m Model) locateLines(lines []viewLine) {
	for start := 0; start < len(lines); {
		if !lines[start].scene {
			start++
			continue
		}
		end := start + 1
		for end < min(len(lines), start+5) && !lines[end].scene && lines[end].place == lines[start].place {
			end++
		}
		for y := start; y < end; y++ {
			lines[y].scene = true
			lines[y].world = y - start
			lines[y].extent = end - start
		}
		start = end
	}
}

func alpineProfile(x float64, seed int) float64 {
	a := 0.18 + float64(seed%19)/100
	b := 0.64 + float64(seed%13)/100
	left := 0.9 * math.Exp(-math.Pow((x-a)/0.16, 2))
	right := 0.72 * math.Exp(-math.Pow((x-b)/0.22, 2))
	shoulder := 0.47 * math.Exp(-math.Pow((x-0.5)/0.13, 2))
	return math.Max(left, math.Max(right, shoulder)) + 0.04*math.Sin(x*71+float64(seed%31))
}

func mountainEdge(surface float64, y int) rune {
	bits := 0
	for side := range 2 {
		for row := range 4 {
			if float64(y)+float64(row+1)/4 >= surface {
				bit := []int{0, 1, 2, 6}[row] + side*3
				if side == 1 && row == 3 {
					bit = 7
				}
				bits |= 1 << bit
			}
		}
	}
	return rune(0x2800 + bits)
}

func (m Model) drawScene(c *alpineCanvas, g expeditionGroup, place string) {
	hue := m.alpine.hues[g.mission]
	jacket := alpineMoss + 1 + hue
	busy := []rune{'λ', 'ʎ'}[m.alpine.frame/6%2]
	dense := g.total() > 6
	type label struct {
		text string
		ink  int
	}
	var labels []label
	if dense {
		for _, state := range []struct {
			count int
			glyph rune
			ink   int
		}{
			{g.counts[core.Busy], busy, jacket},
			{g.counts[core.Waiting], '△', alpineGold},
			{g.counts[core.Idle], '⌂', alpineIce},
			{max(0, g.counts[core.Done]-g.cut), '⚑', alpineGlow},
			{g.counts[core.Unknown], '?', alpineCloud},
			{g.cut, '×', alpineCloud},
		} {
			if state.count > 0 {
				labels = append(labels, label{fmt.Sprintf("%c%d", state.glyph, state.count), state.ink})
			}
		}
	}
	if g.needs > 0 {
		labels = append(labels, label{fmt.Sprintf("!%d", g.needs), alpineFlare})
	}
	labelWidth := max(0, len(labels)-1)
	for _, l := range labels {
		labelWidth += ansi.StringWidth(l.text)
	}
	spacing := 1
	if labelWidth > c.w {
		labelWidth -= len(labels) - 1
		spacing = 0
	}
	landWidth := c.w
	if len(labels) > 0 && (c.h == 1 || !dense) {
		landWidth = max(0, c.w-labelWidth-2)
	}
	terrain := newAlpineCanvas(landWidth, c.h)
	seed := alpineHash(g.mission)
	peak := 0
	tallest := -1.0
	for x := range landWidth {
		u := float64(x) / float64(max(1, landWidth-1))
		profile := alpineProfile(u, seed)
		if profile > tallest {
			tallest, peak = profile, x
		}
		surface := float64(c.h) - profile*float64(c.h)
		for y := range c.h {
			if float64(y+1) <= surface {
				continue
			}
			ink := alpineFar
			if alpineHash(g.region)%4 == 2 {
				ink = alpineFace
			}
			if c.h > 1 && y == int(math.Max(0, surface)) {
				ink = alpineSnow
			}
			terrain.put(x, y, mountainEdge(surface, y), ink)
		}
	}
	if c.h == 1 && g.scale >= 2 {
		center := landWidth / 2
		if g.counts[core.Done] > g.cut {
			center = peak
		}
		for x := range landWidth {
			if x < center-6 || x > center+6 {
				terrain.put(x, 0, ' ', alpineSky)
			}
		}
	}
	floor := c.h - 1
	if dense && c.h > 1 {
		floor--
	}
	for i, e := range g.sample {
		if landWidth == 0 {
			break
		}
		slot := max(1, landWidth/len(g.sample))
		x := min(landWidth-1, i*slot+slot/2)
		y := floor
		glyph, ink := '⌂', alpineIce
		switch {
		case e.cut:
			glyph, ink = '×', alpineCloud
		case e.status == core.Busy:
			step := int(m.alpine.frame/6) + alpineHash(e.id)
			x = min(landWidth-1, x+[]int{0, 1, 1, 0}[step%4])
			profile := alpineProfile(float64(x)/float64(max(1, landWidth-1)), seed)
			y = max(0, floor-int(profile*float64(floor)))
			glyph, ink = []rune{'λ', 'ʎ'}[step%2], jacket
		case e.status == core.Waiting:
			glyph, ink = '△', alpineGold
		case e.status == core.Done:
			x, y, glyph, ink = peak, 0, '⚑', alpineGlow
		case e.status == core.Unknown:
			glyph, ink = '?', alpineCloud
		}
		terrain.put(x, y, glyph, ink)
	}
	if g.needs > 0 && landWidth > 0 {
		glyph := '│'
		if m.alpine.frame%20 < 10 {
			glyph = '✦'
		}
		terrain.put(landWidth-1, 0, glyph, alpineFlare)
	}
	if event, ok := m.alpine.effectAt(place); ok {
		effects := newAlpineCanvas(landWidth, c.h)
		effects.effect(event, peak, 0, m.alpine.frame)
		for i, r := range effects.text {
			if r != ' ' {
				terrain.text[i], terrain.ink[i] = r, effects.ink[i]
			}
		}
	}
	for y := range c.h {
		for x := range landWidth {
			c.put(x, y, terrain.text[y*landWidth+x], terrain.ink[y*landWidth+x])
		}
	}
	if len(labels) > 0 {
		x := c.w - labelWidth
		for xx := max(0, x-1); xx < c.w; xx++ {
			c.put(xx, c.h-1, ' ', alpineSky)
		}
		for _, l := range labels {
			c.label(x, c.h-1, l.text, l.ink)
			x += ansi.StringWidth(l.text) + spacing
		}
	}
}
