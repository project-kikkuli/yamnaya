package tui

import (
	"fmt"
	"hash/fnv"
	"maps"
	"math"
	"slices"
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

func (m Model) sceneMargins() (int, int) {
	if m.alpine.hidden || m.width < 72 || m.height < 18 {
		return 0, 0
	}
	left := 4
	if m.width >= 100 {
		left = 6
	}
	right := 0
	if m.width >= 150 {
		right = min(30, (m.width-120)/2)
	}
	return left, right
}

func (m Model) itemPlace(session string) string {
	if m.alpine.groups["s:"+session] != nil {
		return "s:" + session
	}
	return "m:" + m.board.ProjectOf(session)
}

func (m Model) sceneFocus() string {
	if m.mode == modeWhy && m.why != nil {
		return m.itemPlace(m.why.Session)
	}
	switch m.listMode() {
	case modeAgent:
		return "a:" + m.sess + "/" + m.agent
	case modeSession:
		return "s:" + m.sess
	case modeDetail:
		return "m:" + m.detail
	case modeRepo:
		return "r:" + m.repo
	}
	if r, ok := m.selected(); ok {
		if r.need != nil {
			return "m:" + r.need.Project
		}
		if r.project != nil {
			return "m:" + r.project.Key
		}
		if r.repo != nil {
			return "r:" + r.repo.Key
		}
	}
	if len(m.board.Repos) > 0 {
		return "r:" + m.board.Repos[0].Key
	}
	if len(m.board.Projects) > 0 {
		return "m:" + m.board.Projects[0].Key
	}
	return ""
}

func (m Model) paintView(page viewPage) string {
	left, _ := m.sceneMargins()
	if left == 0 {
		return page.String()
	}
	c := newAlpineCanvas(m.width, len(page.lines))
	// Region coordinates come from identities; scrolling changes the visible slice.
	for y, line := range page.lines {
		g := m.alpine.groups[line.place]
		if g == nil {
			continue
		}
		seed := alpineHash(g.region)
		m.terrainRow(&c, y, line, seed)
	}
	seen := map[string]bool{}
	targets := map[string]int{}
	for y, line := range page.lines {
		g := m.alpine.groups[line.place]
		if g == nil || seen[line.place] {
			continue
		}
		seen[line.place] = true
		m.drawExpedition(&c, y, line, *g)
		if e, ok := m.alpine.effectAt(line.place); ok {
			key := e.kind + e.mountain
			if _, found := targets[key]; !found || strings.HasPrefix(line.place, "m:") {
				targets[key] = y
			}
		}
	}
	for _, y := range slices.Sorted(maps.Values(targets)) {
		line := page.lines[y]
		e, _ := m.alpine.effectAt(line.place)
		m.drawEvent(&c, y, page, e)
	}
	var b strings.Builder
	for y, line := range page.lines {
		if y > 0 {
			b.WriteByte('\n')
		}
		if m.alpine.groups[line.place] == nil {
			b.WriteString(strings.Repeat(" ", line.offset) + line.text)
			continue
		}
		if line.text == "" {
			b.WriteString(c.row(y, 0, c.w))
			continue
		}
		end := min(c.w, line.offset+ansi.StringWidth(line.text))
		b.WriteString(c.row(y, 0, line.offset))
		b.WriteString(line.text)
		b.WriteString(c.row(y, end, c.w))
	}
	return b.String()
}

func (m Model) locateLines(lines []viewLine) {
	for start := 0; start < len(lines); {
		group := m.alpine.groups[lines[start].place]
		if group == nil {
			start++
			continue
		}
		end := start + 1
		for end < len(lines) {
			next := m.alpine.groups[lines[end].place]
			if next == nil || next.region != group.region {
				break
			}
			end++
		}
		for y := start; y < end; y++ {
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

func (m Model) terrainRow(c *alpineCanvas, y int, line viewLine, seed int) {
	t := float64(m.alpine.frame)
	biome := seed % 4
	chapter := line.world / 12
	detailSeed := seed + chapter*7919
	depth := line.world % 12
	height := min(12, max(5, line.extent))
	relief := 0.0
	if g := m.alpine.groups[line.place]; g != nil {
		if i, ok := m.alpine.index[g.mission]; ok {
			relief = m.alpine.mountains[i].height(m.alpine.frame)
		}
	}
	band := line.text == "" && !line.open
	edge := max(line.offset+ansi.StringWidth(line.text)+3, c.w-38)
	if line.text == "" {
		edge = 0
	}
	for x := range c.w {
		if x >= line.offset && x < edge {
			continue
		}
		if x < line.offset {
			river := 1 + int(math.Round(0.8*math.Sin(float64(line.world)*0.4+float64(seed%31))))
			if x == river {
				c.put(x, y, []rune{'╎', '┊', '⠂'}[(line.world+int(m.alpine.frame/9))%3], alpineLake)
			}
			continue
		}
		u := float64(x-(c.w-38)) / 38
		if band || line.open {
			u = float64(x) / float64(c.w)
		}
		profile := alpineProfile(u, detailSeed)
		h, row := float64(height), depth
		if band {
			h, row = 3, line.world%2
		}
		surface := h - 1 - profile*(h-1.5) - relief*0.8
		ink := alpineFar
		if biome == 2 {
			ink = alpineRock
			if u < 0.45 {
				ink = alpineFace
			}
		}
		if biome == 3 {
			ink = alpineFar
			surface += 0.8
		}
		if float64(row+1) > surface && float64(row) < h-1 {
			r := mountainEdge(surface, row)
			if float64(row) > surface+1 {
				r = '█'
			} else if biome != 1 && float64(row)-surface < 0.35 {
				ink = alpineGlow
			}
			if biome == 3 {
				if float64(row)-surface < 0.7 {
					ink = alpineSnow
				} else {
					r = '█'
				}
			}
			c.put(x, y, r, ink)
		}
		if biome == 0 && float64(row) >= h-2 && u > 0.15 && u < 0.87 {
			wave := math.Sin(u*38+t*0.12) + math.Sin(u*13-t*0.06)
			if wave > 0.2 {
				c.put(x, y, '⠤', alpineLake)
			}
			if wave > 1.35 {
				c.put(x, y, '─', alpineWater)
			}
		}
		if biome == 1 {
			grove := math.Sin(u*9+float64(detailSeed%7)) + math.Sin(u*23)
			trunk := 3 + int(float64(height-5)*(0.4+0.2*math.Sin(u*13)))
			if band {
				trunk = 1
			}
			if grove > 0.4 && (x+detailSeed)%4 == 0 {
				if row == trunk-1 {
					c.put(x, y, '♠', alpinePine)
				}
				if row == trunk {
					c.put(x, y, '│', alpineMoss)
				}
			}
			if row >= height-2 && (x+detailSeed)%17 == 0 {
				c.put(x, y, []rune{'·', '⠂', '·'}[(int(t/7)+x)%3], alpineGold)
			}
		}
		if biome == 3 && (x+int(t/3)+depth*7+detailSeed)%37 == 0 {
			c.put(x, y, '⠂', alpineSnow)
		}
		if !band && row < 2 {
			cloud := int(t/7) + detailSeed%31
			if x >= c.w-38+(cloud%30) && x < c.w-38+(cloud%30)+6 {
				c.put(x, y, '⠤', alpineCloud)
			}
		}
	}
}

func (m Model) drawExpedition(c *alpineCanvas, y int, line viewLine, g expeditionGroup) {
	n := g.total()
	if n == 0 && g.needs == 0 {
		return
	}
	seed := alpineHash(g.region)
	hue, ok := m.alpine.hues[g.mission]
	if !ok {
		hue = hueIndex(g.mission)
	}
	jacket := alpineMoss + 1 + hue
	ink := alpineIce
	r := '⌂'
	switch {
	case g.cut > 0 && g.cut == n:
		r = '×'
		ink = alpineCloud
	case g.counts[core.Busy] > 0:
		r = 'λ'
		ink = jacket
	case g.counts[core.Waiting] > 0:
		r = '⌁'
		ink = alpineGold
	case g.counts[core.Unknown] > 0:
		r = '?'
		ink = alpineCloud
	case g.counts[core.Done] == n:
		r = '▴'
		ink = alpineGlow
	}
	badge := string(r)
	if n > 1 {
		badge += fmt.Sprint(n)
	}
	if g.needs > 0 {
		badge = fmt.Sprintf("!%d", g.needs)
		ink = alpineFlare
	}
	if ansi.StringWidth(badge) > line.offset {
		count, glyph := n, r
		if g.needs > 0 {
			count, glyph = g.needs, '!'
		}
		badge = fmt.Sprintf("%c%.0fk", glyph, float64(count)/1000)
	}
	c.label(0, y, badge, ink)

	end := line.offset + ansi.StringWidth(line.text)
	free := c.w - end
	if free < 10 || len(g.sample) == 0 {
		return
	}
	start := max(end+3, c.w-30)
	span := max(1, c.w-start-2)
	if g.needs > 0 {
		x := c.w - 3
		c.put(x, y, '│', alpineFlare)
		if m.alpine.frame%16 < 11 {
			c.put(x, y-1, '✦', alpineFlare)
		}
	}
	if g.scale <= 1 && n > 6 {
		labels := []struct {
			count int
			glyph rune
			ink   int
		}{{g.counts[core.Busy], 'λ', jacket}, {g.counts[core.Waiting], '⌁', alpineGold}, {g.counts[core.Idle], '⌂', alpineIce}, {max(0, g.counts[core.Done]-g.cut), '▴', alpineGlow}, {g.counts[core.Unknown], '?', alpineCloud}, {g.cut, '×', alpineCloud}}
		x := start
		for _, label := range labels {
			if count := label.count; count > 0 {
				word := fmt.Sprintf("%c%d", label.glyph, count)
				if x+len([]rune(word)) > c.w {
					break
				}
				c.label(x, y, word, label.ink)
				x += len([]rune(word)) + 2
			}
		}
		return
	}
	for i, e := range g.sample {
		xx := start + (alpineHash(e.id)+seed)%span
		rr, ii := '⌂', alpineIce
		switch {
		case e.cut:
			rr, ii = '×', alpineCloud
		case e.status == core.Busy:
			xx = start + (alpineHash(e.id)+int(m.alpine.frame/7))%span
			rr, ii = []rune{'λ', 'ʎ'}[(int(m.alpine.frame/3)+i)%2], jacket
		case e.status == core.Waiting:
			rr, ii = '⌁', alpineGold
		case e.status == core.Done:
			rr, ii = '▴', alpineGlow
		case e.status == core.Unknown:
			rr, ii = '?', alpineCloud
		}
		c.put(xx, y, rr, ii)
		if e.status == core.Busy && y > 0 {
			c.put(xx, y-1, 'o', alpineLamp)
		}
	}
}

func (m Model) drawEvent(c *alpineCanvas, y int, page viewPage, e alpineEvent) {
	g := m.alpine.groups[page.lines[y].place]
	if g == nil {
		return
	}
	top, bottom := y, y+1
	for top > 0 {
		prev := m.alpine.groups[page.lines[top-1].place]
		if prev == nil || prev.region != g.region {
			break
		}
		top--
	}
	for bottom < len(page.lines) {
		next := m.alpine.groups[page.lines[bottom].place]
		if next == nil || next.region != g.region {
			break
		}
		bottom++
	}
	height := min(8, bottom-top)
	if height < 1 {
		return
	}
	stage := newAlpineCanvas(24, max(3, height))
	stage.effect(e, 12, 1, m.alpine.frame)
	start := max(top, min(y-2, bottom-height))
	x := c.w - 24
	free := c.w - page.lines[y].offset - ansi.StringWidth(page.lines[y].text)
	if free < 18 {
		x = -9
	}
	for yy := 0; yy < height; yy++ {
		for xx := range stage.w {
			if r := stage.text[yy*stage.w+xx]; r != ' ' {
				c.put(x+xx, start+yy, r, stage.ink[yy*stage.w+xx])
			}
		}
	}
}
