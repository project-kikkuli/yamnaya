package tui

import (
	"hash/fnv"
	"os"
	"slices"

	"charm.land/lipgloss/v2"
)

// palette keeps board colors in the terminal theme.
// red is what needs you or fails, yellow what waits, green what runs or passes, faint what is stale or exited.
type palette struct {
	plain, bold, dim   lipgloss.Style
	red, yellow, green lipgloss.Style
	// hues are the colors no state uses, one per project.
	hues []lipgloss.Style
	// color is false under NO_COLOR, which also leaves the mountains out.
	color bool
}

var hueColors = []string{"4", "5", "6", "12", "13", "14"}

var pal = defaultPalette()

// defaultPalette draws on the screen; NO_COLOR drops color and keeps bold and faint.
func defaultPalette() palette {
	return newPalette(os.Getenv("NO_COLOR") == "")
}

func newPalette(color bool) palette {
	fg := func(c string) lipgloss.Style {
		s := lipgloss.NewStyle()
		if color {
			s = s.Foreground(lipgloss.Color(c))
		}
		return s
	}
	p := palette{plain: lipgloss.NewStyle(), bold: lipgloss.NewStyle().Bold(true), dim: lipgloss.NewStyle().Faint(true), red: fg("1"), yellow: fg("3"), green: fg("2"), color: color}
	for _, c := range hueColors {
		p.hues = append(p.hues, fg(c))
	}
	return p
}

// hue is the project's own color, derived from its key so it never needs storing.
func (p palette) hue(key string) lipgloss.Style {
	return p.hues[hueIndex(key)]
}

// hueSlots gives each project its own hue, or the next one no other project on the board holds, sharing only once all are taken.
func hueSlots(keys []string) map[string]int {
	slots, taken := map[string]int{}, map[int]bool{}
	for _, k := range slices.Sorted(slices.Values(keys)) {
		if len(taken) == len(hueColors) {
			taken = map[int]bool{}
		}
		i := hueIndex(k)
		for taken[i] {
			i = (i + 1) % len(hueColors)
		}
		slots[k], taken[i] = i, true
	}
	return slots
}

func hueIndex(key string) int {
	h := fnv.New32a()
	h.Write([]byte(key))
	return int(h.Sum32() % uint32(len(hueColors)))
}

// state colors a session or mission state word.
func (p palette) state(s string) lipgloss.Style {
	switch s {
	case "busy":
		return p.green
	case "waiting":
		return p.yellow
	case "adrift", "hung":
		return p.red
	case "exited":
		return p.dim
	}
	return p.plain
}
