package tui

import (
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func alpineHash(s string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return int(h.Sum32())
}

func (m Model) alpineView(height int) string {
	a := m.alpine
	c := newAlpineCanvas(m.width, height-2)
	t := float64(a.frame)
	base := float64(c.h - 2)
	// The far range remains continuous as the expedition camera moves between valleys.
	for x := range c.w {
		surface := base - 1.5 - math.Abs(2*math.Sin(float64(x)*0.051)) - 0.6*math.Sin(float64(x)*0.17)
		c.mountainColumn(x, surface, base, alpineFar, false)
	}
	for i := range max(2, c.w/25) {
		x := (i*37 + 9) % c.w
		y := i % max(1, c.h/4)
		r := '·'
		if (a.frame/17+uint64(i))%7 == 0 {
			r = '✧'
		}
		c.put(x, y, r, alpineIce)
	}
	start, end := m.alpineWindow()
	count := max(1, end-start)
	span := float64(c.w) / float64(count)
	keys := make([]string, len(m.board.Projects))
	for i, p := range m.board.Projects {
		keys[i] = p.Key
	}
	hues := hueSlots(keys)
	for i := start; i < end; i++ {
		p := a.mountains[i]
		hue := alpineStorm + 1 + hues[p.key]
		center := (float64(i-start) + 0.5) * span
		seed := alpineHash(p.key)
		top := 1.4 + float64(seed%9)/13 - p.relief*0.7
		for x := max(0, int(center-span*0.72)); x < min(c.w, int(center+span*0.72)); x++ {
			d := (float64(x) - center) / (span * 0.66)
			surface := top + math.Abs(d)*(base-top)
			shoulder := center + span*(0.18+float64(seed%3)*0.05)
			if seed%2 == 0 {
				shoulder = center - span*0.27
			}
			surface = math.Min(surface, top+1.1+math.Abs(float64(x)-shoulder)/(span*0.42)*(base-top))
			surface += 0.32*math.Sin(float64(x)*0.7+float64(seed%31)) + 0.12*math.Sin(float64(x)*2.1)
			ink := alpineRock
			if float64(x) < center {
				ink = alpineFace
			}
			c.mountainColumn(x, surface, base, ink, surface < top+1.8)
		}
		// Snow carries sideways while the mountain's shape stays anchored.
		for j := range 5 {
			x := int(center) + (int(a.frame/2)+j*5)%13 - 6
			y := int(top) - 1 + j%2
			if y >= 0 {
				c.put(x, y, '⠂', alpineSnow)
			}
		}
		campX, campY := int(center-span*0.3), int(base)-1
		if p.active && !a.storming(p.key) {
			c.label(campX, campY, "╱╲", hue)
			c.put(campX+3, campY, '⚑', alpineGlow)
		}
		teams := a.onMountain(p.key)
		for j, e := range teams {
			if a.swept(e.session) {
				continue
			}
			u := float64(alpineHash(e.id)%100) / 100
			if e.status == core.Busy {
				u = math.Mod(u+t/190, 1)
			}
			x := int(center - span*0.35 + u*span*0.65)
			y := int(top + math.Abs(float64(x)-center)/(span*0.66)*(base-top) + 0.5)
			y = max(2, min(c.h-3, y))
			ink := alpineLamp
			switch {
			case e.cut:
				c.put(x, y, '×', alpineCloud)
			case e.status == core.Done:
				c.put(int(center)+j%3-1, int(top), '▴', alpineGlow)
			case e.status == core.Busy:
				c.put(x, y-1, 'o', ink)
				c.put(x, y, []rune{'λ', 'ʎ'}[(a.frame/3+uint64(j))%2], hue)
			case e.status == core.Waiting:
				c.label(x, y, "⌁", alpineGold)
			case e.status == core.Unknown:
				c.put(x, campY, '?', alpineCloud)
			default:
				c.put(x, campY, 'o', alpineIce)
			}
		}
		if p.needs > 0 {
			x := int(center + span*0.22)
			y := 1 + int((a.frame/3)%uint64(max(1, c.h/2)))
			c.put(x, y, '│', alpineFlare)
			c.put(x, y-1, '✦', alpineFlare)
		}
		for _, e := range a.events {
			if e.mountain == p.key {
				c.effect(e, int(center), int(top), a.frame)
			}
		}
		name := ansi.Truncate(strings.ReplaceAll(p.name, "\n", " "), max(1, int(span)-3), "…")
		c.label(int(float64(i-start)*span)+2, c.h-1, name, hue)
	}
	// Clouds drift on their own clock, including when every expedition rests.
	for i := range max(1, c.w/35) {
		x := (i*43+int(a.frame/6))%(c.w+15) - 12
		c.label(x, i%2, "⠤⠤⠴⠶⠶⠤⠤", alpineCloud)
	}
	for x := range c.w {
		for y := c.h - 2; y < c.h-1; y++ {
			wave := math.Sin(float64(x)*0.31+t*0.11+float64(y)*2) + math.Sin(float64(x)*0.09-t*0.07)
			r, ink := ' ', alpineSky
			if wave > 0.6 {
				r, ink = '⠤', alpineLake
				if wave > 1.5 {
					r, ink = '─', alpineWater
				}
			}
			c.put(x, y, r, ink)
		}
	}
	caption := " THE RANGE"
	for i := len(a.events) - 1; i >= 0; i-- {
		e := a.events[i]
		if e.kind == "storm" || e.kind == "summit" {
			word := "SUMMIT"
			if e.kind == "storm" {
				word = "TEAM SWEPT AWAY"
			}
			caption += " / " + word
			break
		}
	}
	if end > start {
		caption += fmt.Sprintf("  %d–%d/%d", start+1, end, len(a.mountains))
	}
	caption += "   " + a.population()
	if selected := m.alpineSelected(); selected != "" {
		for _, p := range a.mountains {
			if p.key == selected {
				caption += "   / " + strings.ReplaceAll(p.name, "\n", " ")
			}
		}
	}
	return c.String() + pal.dim.Render(ansi.Truncate(caption, m.width, "…")) + "\n"
}

func (c *alpineCanvas) mountainColumn(x int, surface, base float64, ink int, snow bool) {
	for y := max(0, int(surface)); y < min(c.h-2, int(base)); y++ {
		if surface >= float64(y+1) {
			continue
		}
		r, shade := '█', ink
		if y == int(surface) {
			r = []rune{'⣿', '⣾', '⣶', '⣤'}[min(3, max(0, int((surface-float64(y))*4)))]
			if snow {
				shade = alpineGlow
			}
		} else if snow && float64(y)-surface < 0.9 {
			shade = alpineSnow
		} else if (x+y*3)%13 == 0 {
			shade = alpineRock
		}
		c.put(x, y, r, shade)
	}
}

func (a alpine) onMountain(key string) []expedition {
	var out []expedition
	for _, e := range a.teams {
		if e.mountain == key {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b expedition) int { return strings.Compare(a.id, b.id) })
	return out
}

func (a alpine) swept(session string) bool {
	for _, e := range a.events {
		if e.kind == "storm" && e.team == session {
			return true
		}
	}
	return false
}

func (a alpine) storming(mountain string) bool {
	for _, e := range a.events {
		if e.kind == "storm" && e.mountain == mountain {
			return true
		}
	}
	return false
}

func (a alpine) population() string {
	counts := map[core.Status]int{}
	for _, e := range a.teams {
		counts[e.status]++
	}
	var parts []string
	for _, v := range []struct {
		status core.Status
		label  string
	}{{core.Busy, "climbing"}, {core.Waiting, "sheltering"}, {core.Idle, "at camp"}, {core.Done, "ended"}, {core.Unknown, "unknown"}} {
		if n := counts[v.status]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", n, v.label))
		}
	}
	if len(parts) == 0 {
		return "no expeditions"
	}
	return strings.Join(parts, " · ")
}

func (m Model) alpineSelected() string {
	if r, ok := m.selected(); ok {
		if r.need != nil {
			return r.need.Project
		}
		if r.project != nil {
			return r.project.Key
		}
		if r.repo != nil && len(r.repo.Missions) > 0 {
			return r.repo.Missions[0].Key
		}
		if p := m.projectOf(r); p != nil {
			return p.Key
		}
	}
	return m.detail
}

func (m Model) alpineWindow() (int, int) {
	n := len(m.alpine.mountains)
	capacity := max(1, m.width/26)
	selected := m.alpineSelected()
	for i := len(m.alpine.events) - 1; i >= 0; i-- {
		e := m.alpine.events[i]
		if e.kind == "storm" || e.kind == "summit" {
			selected = e.mountain
			break
		}
	}
	start := 0
	for i, p := range m.alpine.mountains {
		if p.key == selected {
			start = min(max(0, i-capacity/2), max(0, n-capacity))
			break
		}
	}
	return start, min(n, start+capacity)
}
