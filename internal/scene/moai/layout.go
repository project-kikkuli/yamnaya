package moai

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	// bandRows is one terrace of ahu: names, air, three rows of moai, the ahu, grass, the cove and the site line.
	bandRows = 9
	// overhead is an ahu's platform without moai: its end stones.
	overhead = 4
	minSlot  = 11
	maxSlot  = 24
	gap      = 2
)

// slot is a moai's place on an ahu: one unit, or several folded together when the screen is crowded.
type slot struct {
	id, name string
	// hit is what a click on it selects: the unit, or the ahu when several units are folded into it.
	hit      string
	weather  scene.Weather
	team     []scene.Climber
	camps    []scene.Camp
	needs    bool
	progress float64
	dying    float64
	selected bool
	units    int
	x, w     int
}

// ahu is a stone platform: a group's units, or several groups folded together.
type ahu struct {
	id, name string
	sky      scene.Weather
	selected bool
	// site is the top-level chief whose ceremonial site holds the ahu, "" for a repository's own.
	site, siteName string
	siteSelected   bool
	slots          []slot
	ahus           int
	x, w, band     int
}

func (s ahu) width() int {
	w := overhead
	for _, q := range s.slots {
		w += q.w
	}
	return w
}

// bracket is a ceremonial site's line under its ahu on one band.
type bracket struct {
	id, name string
	selected bool
	x0, x1   int
	band     int
}

type layout struct {
	ahus  []ahu
	sites []bracket
	bands int
	// top is the first band's row.
	top int
}

func (l layout) row(band int) int { return l.top + band*bandRows }

// rows is where the bands may stand: below a strip of open sky and above the low middle the table covers.
func rows(height int) (sky, limit int) {
	return max(1, height/12), max(bandRows, height*62/100)
}

func plan(s scene.Scene, width, height int) layout {
	var ahus []ahu
	for _, r := range s.Ranges {
		site, name := "", ""
		if !strings.HasPrefix(r.ID, "repo:") {
			site, name = r.ID, r.Name
		}
		ahus = collect(ahus, r, site, name, s.Selected)
	}
	if len(s.Peaks) > 0 {
		ahus = append(ahus, ahuOf("", "", scene.Combine(scene.Clear, weathers(s.Peaks)), s.Peaks, s.Selected))
	}
	sky, limit := rows(height)
	most := max(1, (limit-sky)/bandRows)
	l := layout{}
	l.ahus, l.bands = fit(ahus, width, most)
	l.top = max(0, limit-l.bands*bandRows)
	l.place(width)
	return l
}

func weathers(ps []scene.Peak) []scene.Weather {
	out := make([]scene.Weather, len(ps))
	for i, p := range ps {
		out[i] = p.Weather
	}
	return out
}

func collect(ahus []ahu, r scene.Range, site, siteName, sel string) []ahu {
	if len(r.Peaks) > 0 || len(r.Ranges) == 0 {
		sh := ahuOf(r.ID, r.Name, r.Sky(), r.Peaks, sel)
		sh.site, sh.siteName, sh.siteSelected = site, siteName, site != "" && site == sel
		ahus = append(ahus, sh)
	}
	for _, sub := range r.Ranges {
		ahus = collect(ahus, sub, site, siteName, sel)
	}
	return ahus
}

func ahuOf(id, name string, sky scene.Weather, peaks []scene.Peak, sel string) ahu {
	sh := ahu{id: id, name: name, sky: sky, selected: id != "" && id == sel, ahus: 1}
	for _, p := range peaks {
		progress := p.Progress
		if math.IsNaN(progress) {
			progress = 0
		}
		sh.slots = append(sh.slots, slot{id: p.ID, name: p.Name, hit: p.ID, weather: p.Weather, team: p.Team, camps: p.Camps,
			needs: p.NeedsYou, progress: scene.Clamp(progress, 0, 1), dying: scene.Clamp(p.Dying, 0, 1), selected: p.ID == sel, units: 1})
	}
	return sh
}

// want is how wide a moai's place would like to be: room for its name, its stones and its carvers.
func (q slot) want() int {
	return min(maxSlot, max(minSlot, utf8.RuneCountInString(q.name)+6, 8+len(q.team), 2+2*len(q.camps)))
}

// fit lays the ahu out on as few bands as fit them at the width they want, and when even every band is too
// narrow, folds moai together on each ahu, then ahu together, until they fit.
func fit(ahus []ahu, width, most int) ([]ahu, int) {
	sized := func(ahus []ahu, wide bool) []ahu {
		out := make([]ahu, len(ahus))
		for i, sh := range ahus {
			sh.slots = append([]slot(nil), sh.slots...)
			for k := range sh.slots {
				sh.slots[k].w = minSlot
				if wide {
					sh.slots[k].w = sh.slots[k].want()
				}
			}
			out[i] = sh
		}
		return out
	}
	for b := 1; b <= most; b++ {
		if out := sized(ahus, true); pack(out, width, b) {
			return out, b
		}
	}
	if out := sized(ahus, false); pack(out, width, most) {
		return out, most
	}
	widest := 1
	for _, sh := range ahus {
		widest = max(widest, len(sh.slots))
	}
	for k := widest - 1; k >= 1; k-- {
		folded := make([]ahu, len(ahus))
		for i, sh := range ahus {
			folded[i] = sh
			folded[i].slots = foldSlots(sh, k)
		}
		if out := sized(folded, false); pack(out, width, most) {
			return out, most
		}
	}
	for n := len(ahus) - 1; n >= 1; n-- {
		var merged []ahu
		for i := range n {
			merged = append(merged, foldAhus(ahus[i*len(ahus)/n:(i+1)*len(ahus)/n]))
		}
		if out := sized(merged, false); pack(out, width, most) || n == 1 {
			pack(out, width, most)
			return out, most
		}
	}
	out := sized(ahus, false)
	pack(out, width, most)
	return out, most
}

// pack puts each ahu on a band in order, reporting whether they all fit on bands bands.
func pack(ahus []ahu, width, bands int) bool {
	band, used := 0, 1
	for i := range ahus {
		w := ahus[i].width()
		if f := ahus[i].site; f != "" && used > 1 && (i == 0 || ahus[i-1].site != f) {
			run := w
			for k := i + 1; k < len(ahus) && ahus[k].site == f; k++ {
				run += gap + ahus[k].width()
			}
			if used+run+1 > width && run+2 <= width {
				band, used = band+1, 1
			}
		}
		if used+w+1 > width && used > 1 {
			band, used = band+1, 1
		}
		if band >= bands || used+w+1 > width {
			return false
		}
		ahus[i].band = band
		used += w + gap
	}
	return true
}

func foldSlots(sh ahu, k int) []slot {
	if len(sh.slots) <= k {
		return sh.slots
	}
	out := make([]slot, k)
	for i := range k {
		out[i] = merge(sh.slots[i*len(sh.slots)/k:(i+1)*len(sh.slots)/k], sh.id)
	}
	return out
}

// merge folds moai into one carrying their carvers, stones, alerts and selection; a click on it selects hit.
func merge(qs []slot, hit string) slot {
	if len(qs) == 1 {
		return qs[0]
	}
	out := slot{id: qs[0].id + "+", hit: hit}
	parts := make([]scene.Weather, 0, len(qs))
	for _, q := range qs {
		out.team = append(out.team, q.team...)
		out.camps = append(out.camps, q.camps...)
		out.needs = out.needs || q.needs
		out.selected = out.selected || q.selected
		out.progress += q.progress * float64(q.units)
		out.units += q.units
		parts = append(parts, q.weather)
	}
	out.progress /= float64(out.units)
	out.weather = scene.Combine(scene.Clouds, parts)
	out.name = strconv.Itoa(out.units) + " moai"
	return out
}

func foldAhus(ahus []ahu) ahu {
	if len(ahus) == 1 {
		sh := ahus[0]
		sh.slots = foldSlots(sh, 1)
		return sh
	}
	out := ahu{id: "", site: ahus[0].site, siteName: ahus[0].siteName}
	var all []slot
	parts := make([]scene.Weather, 0, len(ahus))
	for _, sh := range ahus {
		all = append(all, sh.slots...)
		out.selected = out.selected || sh.selected || holds(sh)
		out.siteSelected = out.siteSelected || sh.siteSelected
		out.ahus += sh.ahus
		parts = append(parts, sh.sky)
		if sh.site != out.site {
			out.site, out.siteName = "", ""
		}
	}
	out.sky = scene.Combine(scene.Clouds, parts)
	out.name = strconv.Itoa(out.ahus) + " ahu"
	if len(all) > 0 {
		q := merge(all, "")
		q.selected = false
		out.slots = []slot{q}
	}
	return out
}

func holds(sh ahu) bool {
	for _, q := range sh.slots {
		if q.selected {
			return true
		}
	}
	return false
}

// place spreads each band's ahu across the width, giving moai what room is left up to what they want.
func (l *layout) place(width int) {
	for b := range l.bands {
		var on []int
		used, slots := 0, 0
		for i, sh := range l.ahus {
			if sh.band == b {
				on = append(on, i)
				used += sh.width()
				slots += len(sh.slots)
			}
		}
		if len(on) == 0 {
			continue
		}
		spare := width - used - gap*(len(on)-1) - 2
		for spare > 0 && slots > 0 {
			grew := false
			for _, i := range on {
				for k := range l.ahus[i].slots {
					q := &l.ahus[i].slots[k]
					if spare > 0 && q.w < max(q.want(), maxSlot*2/3) {
						q.w++
						spare--
						grew = true
					}
				}
			}
			if !grew {
				break
			}
		}
		used = 0
		for _, i := range on {
			used += l.ahus[i].width()
		}
		space := max(gap, (width-used)/(len(on)+1))
		x := max(1, (width-used-space*(len(on)-1))/2)
		for _, i := range on {
			sh := &l.ahus[i]
			sh.x, sh.w = x, sh.width()
			at := x + 2
			for k := range sh.slots {
				sh.slots[k].x = at
				at += sh.slots[k].w
			}
			x += sh.w + space
		}
	}
	for i := 0; i < len(l.ahus); {
		sh := l.ahus[i]
		j := i + 1
		for j < len(l.ahus) && sh.site != "" && l.ahus[j].site == sh.site && l.ahus[j].band == sh.band {
			j++
		}
		if sh.site != "" {
			last := l.ahus[j-1]
			l.sites = append(l.sites, bracket{id: sh.site, name: sh.siteName, selected: sh.siteSelected, x0: sh.x, x1: last.x + last.w, band: sh.band})
		}
		i = j
	}
}
