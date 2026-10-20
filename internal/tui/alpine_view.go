package tui

import (
	"hash/fnv"
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
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

func (m Model) scenery() bool {
	if m.alpine.hidden || !m.loaded || m.width < 72 || m.height < 24 {
		return false
	}
	switch m.mode {
	case modeHelp, modeScreen, modeWhy, modePick, modeItems:
		return false
	case modeInput:
		if m.inKind == inNewRepo {
			return false
		}
	}
	return true
}

func (m Model) sceneWidth() int {
	if !m.scenery() || m.width < 148 || m.height < 30 {
		return 0
	}
	return 44
}

func (m Model) skylineHeight() int {
	if !m.scenery() {
		return 0
	}
	if m.height >= 36 {
		return 8
	}
	if m.height >= 30 {
		return 6
	}
	return 5
}

func (m Model) paintView(page viewPage) string {
	l := page.layout
	sky := newAlpineCanvas(m.width, l.sky)
	if l.sky > 0 {
		m.drawPanorama(&sky, l)
	}
	panel := newAlpineCanvas(l.panelWidth, l.panelHeight)
	if l.panelHeight > 0 {
		m.drawPanel(&panel, l.focusGroup, l.focus)
	}
	lake := newAlpineCanvas(m.width, l.lakeHeight)
	if l.lakeHeight > 0 {
		m.drawLake(&lake, l)
	}
	var b strings.Builder
	for y, line := range page.lines {
		if y > 0 {
			b.WriteByte('\n')
		}
		switch line.scenery {
		case 1:
			b.WriteString(sky.row(line.world, 0, sky.w))
		case 2:
			b.WriteString(lake.row(line.world, 0, lake.w))
		default:
			b.WriteString(line.text)
			if y >= l.panelStart && y < l.panelStart+l.panelHeight {
				b.WriteString(strings.Repeat(" ", m.width-l.panelWidth-ansi.StringWidth(line.text)))
				b.WriteString(panel.row(y-l.panelStart, 0, panel.w))
			}
		}
	}
	return b.String()
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
