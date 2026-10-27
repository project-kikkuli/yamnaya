package civilizations

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	// bandRows is one row of continents: labels on the empire's top border, five rows of land, the bottom border.
	bandRows = 7
	// overhead is a continent's coast either side of its civilizations.
	overhead = 2
	minCiv   = 11
	maxCiv   = 24
	gap      = 2
)

// civ is a civilization on a continent: one unit, or several folded together when the screen is crowded.
type civ struct {
	id, name string
	// hit is what a click on it selects: the unit, or the continent when several units are folded into it.
	hit      string
	weather  scene.Weather
	team     []scene.Climber
	camps    []scene.Camp
	needs    bool
	progress float64
	dying    float64
	selected bool
	units    int
	tint     int
	x, w     int
}

// land is a continent: a group's units, or several groups folded together.
type land struct {
	id, name string
	sky      scene.Weather
	selected bool
	// empire is the top-level chief whose border holds the continent, "" for a repository's own.
	empire, empireName string
	empireSelected     bool
	empireSky          scene.Weather
	// nested marks a chief's continent inside another chief's empire, walled by a dashed border of its own.
	nested     bool
	civs       []civ
	lands      int
	x, w, band int
}

func (n land) width() int {
	w := overhead
	for _, c := range n.civs {
		w += c.w
	}
	return w
}

// empire is a chief's dashed border round its continents on one band.
type empire struct {
	id, name string
	sky      scene.Weather
	selected bool
	x0, x1   int
	band     int
}

type layout struct {
	lands   []land
	empires []empire
	bands   int
	top     int
	step    int
}

func (l layout) row(band int) int { return l.top + band*l.step }

// limit is the last row continents may use: above the low middle the table covers.
func limit(height int) int { return max(bandRows, height*62/100) }

func plan(s scene.Scene, width, height int) layout {
	var lands []land
	for _, r := range s.Ranges {
		emp, name := "", ""
		if !strings.HasPrefix(r.ID, "repo:") {
			emp, name = r.ID, r.Name
		}
		start := len(lands)
		lands = collect(lands, r, emp, name, false, s.Selected)
		for i := start; i < len(lands); i++ {
			lands[i].empireSky = r.Sky()
		}
	}
	if len(s.Peaks) > 0 {
		lands = append(lands, landOf("", "", scene.Combine(scene.Clear, weathers(s.Peaks)), s.Peaks, s.Selected))
	}
	rows := limit(height)
	most := max(1, rows/bandRows)
	l := layout{}
	l.lands, l.bands = fit(lands, width, most)
	spare := rows - l.bands*bandRows
	vg := 0
	if l.bands > 1 {
		vg = min(3, spare/(l.bands+1))
	}
	l.step = bandRows + vg
	l.top = max(0, (rows-l.bands*bandRows-vg*(l.bands-1))/2)
	l.place(width)
	for i := range l.lands {
		for k := range l.lands[i].civs {
			l.lands[i].civs[k].tint = (k + i*2) % len(tintHexDay)
		}
	}
	return l
}

func weathers(ps []scene.Peak) []scene.Weather {
	out := make([]scene.Weather, len(ps))
	for i, p := range ps {
		out[i] = p.Weather
	}
	return out
}

func collect(lands []land, r scene.Range, emp, empName string, nested bool, sel string) []land {
	if len(r.Peaks) > 0 || len(r.Ranges) == 0 {
		n := landOf(r.ID, r.Name, r.Sky(), r.Peaks, sel)
		n.empire, n.empireName, n.empireSelected, n.nested = emp, empName, emp != "" && emp == sel, nested
		lands = append(lands, n)
	}
	for _, sub := range r.Ranges {
		lands = collect(lands, sub, emp, empName, emp != "", sel)
	}
	return lands
}

func landOf(id, name string, sky scene.Weather, peaks []scene.Peak, sel string) land {
	n := land{id: id, name: name, sky: sky, selected: id != "" && id == sel, lands: 1}
	for _, p := range peaks {
		progress := p.Progress
		if math.IsNaN(progress) {
			progress = 0
		}
		n.civs = append(n.civs, civ{id: p.ID, name: p.Name, hit: p.ID, weather: p.Weather, team: p.Team, camps: p.Camps,
			needs: p.NeedsYou, progress: scene.Clamp(progress, 0, 1), dying: scene.Clamp(p.Dying, 0, 1), selected: p.ID == sel, units: 1})
	}
	return n
}

// want is how wide a civilization would like to be: room for its name and percentage, and its cities in pairs.
func (c civ) want() int {
	cities := 5 + (len(c.camps)+1)/2*2
	return min(maxCiv, max(minCiv, utf8.RuneCountInString(c.name)+6, cities))
}

// fit lays the continents out on as few bands as fit them at the width they want, and when even every band is
// too narrow, folds civilizations together on each continent, then continents together, until they fit.
func fit(lands []land, width, most int) ([]land, int) {
	sized := func(lands []land, wide bool) []land {
		out := make([]land, len(lands))
		for i, n := range lands {
			n.civs = append([]civ(nil), n.civs...)
			for k := range n.civs {
				n.civs[k].w = minCiv
				if wide {
					n.civs[k].w = n.civs[k].want()
				}
			}
			out[i] = n
		}
		return out
	}
	for b := 1; b <= most; b++ {
		if out := sized(lands, true); pack(out, width, b) {
			return out, b
		}
	}
	if out := sized(lands, false); pack(out, width, most) {
		return out, most
	}
	widest := 1
	for _, n := range lands {
		widest = max(widest, len(n.civs))
	}
	for k := widest - 1; k >= 1; k-- {
		folded := make([]land, len(lands))
		for i, n := range lands {
			folded[i] = n
			folded[i].civs = foldCivs(n, k)
		}
		if out := sized(folded, false); pack(out, width, most) {
			return out, most
		}
	}
	for n := len(lands) - 1; n >= 1; n-- {
		var merged []land
		for i := range n {
			merged = append(merged, foldLands(lands[i*len(lands)/n:(i+1)*len(lands)/n]))
		}
		if out := sized(merged, false); pack(out, width, most) || n == 1 {
			pack(out, width, most)
			return out, most
		}
	}
	out := sized(lands, false)
	pack(out, width, most)
	return out, most
}

// pack puts each continent on a band in order, an empire's together where they fit, reporting whether they all
// fit on bands bands.
func pack(lands []land, width, bands int) bool {
	band, used := 0, 1
	for i := range lands {
		w := lands[i].width()
		if e := lands[i].empire; e != "" && used > 1 && (i == 0 || lands[i-1].empire != e) {
			run := w
			for k := i + 1; k < len(lands) && lands[k].empire == e; k++ {
				run += gap + lands[k].width()
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
		lands[i].band = band
		used += w + gap
	}
	return true
}

func foldCivs(n land, k int) []civ {
	if len(n.civs) <= k {
		return n.civs
	}
	out := make([]civ, k)
	for i := range k {
		out[i] = merge(n.civs[i*len(n.civs)/k:(i+1)*len(n.civs)/k], n.id)
	}
	return out
}

// merge folds civilizations into one carrying their agents, cities, alerts and selection; a click on it selects hit.
func merge(cs []civ, hit string) civ {
	if len(cs) == 1 {
		return cs[0]
	}
	out := civ{id: cs[0].id + "+", hit: hit}
	parts := make([]scene.Weather, 0, len(cs))
	for _, c := range cs {
		out.team = append(out.team, c.team...)
		out.camps = append(out.camps, c.camps...)
		out.needs = out.needs || c.needs
		out.selected = out.selected || c.selected
		out.progress += c.progress * float64(c.units)
		out.units += c.units
		parts = append(parts, c.weather)
	}
	out.progress /= float64(out.units)
	out.weather = scene.Combine(scene.Clouds, parts)
	out.name = strconv.Itoa(out.units) + " civs"
	return out
}

func foldLands(lands []land) land {
	if len(lands) == 1 {
		n := lands[0]
		n.civs = foldCivs(n, 1)
		return n
	}
	out := land{empire: lands[0].empire, empireName: lands[0].empireName, empireSky: lands[0].empireSky}
	var all []civ
	parts := make([]scene.Weather, 0, len(lands))
	for _, n := range lands {
		all = append(all, n.civs...)
		out.selected = out.selected || n.selected || holds(n)
		out.empireSelected = out.empireSelected || n.empireSelected
		out.lands += n.lands
		parts = append(parts, n.sky)
		if n.empire != out.empire {
			out.empire, out.empireName = "", ""
		}
	}
	out.sky = scene.Combine(scene.Clouds, parts)
	out.name = strconv.Itoa(out.lands) + " continents"
	if len(all) > 0 {
		c := merge(all, "")
		c.selected = false
		out.civs = []civ{c}
	}
	return out
}

func holds(n land) bool {
	for _, c := range n.civs {
		if c.selected {
			return true
		}
	}
	return false
}

// place spreads each band's continents across the width, giving civilizations what room is left up to what
// they want, and draws each empire's border round its continents on the band.
func (l *layout) place(width int) {
	for b := range l.bands {
		var on []int
		used, civs := 0, 0
		for i, n := range l.lands {
			if n.band == b {
				on = append(on, i)
				used += n.width()
				civs += len(n.civs)
			}
		}
		if len(on) == 0 {
			continue
		}
		spare := width - used - gap*(len(on)-1) - 2
		for spare > 0 && civs > 0 {
			grew := false
			for _, i := range on {
				for k := range l.lands[i].civs {
					c := &l.lands[i].civs[k]
					if spare > 0 && c.w < max(c.want(), maxCiv*2/3) {
						c.w++
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
			used += l.lands[i].width()
		}
		space := max(gap, (width-used)/(len(on)+1))
		x := max(1, (width-used-space*(len(on)-1))/2)
		for _, i := range on {
			n := &l.lands[i]
			n.x, n.w = x, n.width()
			at := x + overhead/2
			for k := range n.civs {
				n.civs[k].x = at
				at += n.civs[k].w
			}
			x += n.w + space
		}
	}
	for i := 0; i < len(l.lands); {
		n := l.lands[i]
		j := i + 1
		for j < len(l.lands) && n.empire != "" && l.lands[j].empire == n.empire && l.lands[j].band == n.band {
			j++
		}
		if n.empire != "" {
			last := l.lands[j-1]
			l.empires = append(l.empires, empire{id: n.empire, name: n.empireName, sky: n.empireSky, selected: n.empireSelected, x0: n.x - 1, x1: last.x + last.w, band: n.band})
		}
		i = j
	}
}
