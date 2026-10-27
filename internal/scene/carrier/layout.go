package carrier

import (
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	// bandRows is one row of carriers: names, air, flight deck, hull, waterline and the sea under it.
	bandRows = 6
	// overhead is a carrier's deck without squadrons: stern, island, a gap, and the bow.
	overhead = 7
	minSquad = 10
	maxSquad = 26
	gap      = 2
)

// squad is a squadron on a carrier's deck: one unit, or several folded together when the screen is crowded.
type squad struct {
	id, name string
	// hit is what a click on it selects: the unit, or the carrier when several units are folded into it.
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

// ship is a carrier: a group's units, or several groups folded together.
type ship struct {
	id, name string
	sky      scene.Weather
	selected bool
	// fleet is the top-level chief the carrier sails with, "" for a repository's carrier.
	fleet, fleetName string
	fleetSelected    bool
	squads           []squad
	ships            int
	x, w, band       int
}

func (s ship) width() int {
	w := overhead
	for _, q := range s.squads {
		w += q.w
	}
	return w
}

// bracket is a fleet's line under its carriers in formation on one band.
type bracket struct {
	id, name string
	selected bool
	x0, x1   int
	band     int
}

type layout struct {
	ships  []ship
	fleets []bracket
	bands  int
	// top is the first band's row.
	top int
}

func (l layout) row(band int) int { return l.top + band*bandRows }

// rows is where the bands may stand: below a strip of open sky and above the low middle the table covers.
func rows(height int) (sky, limit int) {
	return max(1, height/12), max(bandRows, height*62/100)
}

func plan(s scene.Scene, width, height int) layout {
	var ships []ship
	for _, r := range s.Ranges {
		fleet, name := "", ""
		if !strings.HasPrefix(r.ID, "repo:") {
			fleet, name = r.ID, r.Name
		}
		ships = collect(ships, r, fleet, name, s.Selected)
	}
	if len(s.Peaks) > 0 {
		ships = append(ships, shipOf("", "", scene.Combine(scene.Clear, weathers(s.Peaks)), s.Peaks, s.Selected))
	}
	sky, limit := rows(height)
	most := max(1, (limit-sky)/bandRows)
	l := layout{}
	l.ships, l.bands = fit(ships, width, most)
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

func collect(ships []ship, r scene.Range, fleet, fleetName, sel string) []ship {
	if len(r.Peaks) > 0 || len(r.Ranges) == 0 {
		sh := shipOf(r.ID, r.Name, r.Sky(), r.Peaks, sel)
		sh.fleet, sh.fleetName, sh.fleetSelected = fleet, fleetName, fleet != "" && fleet == sel
		ships = append(ships, sh)
	}
	for _, sub := range r.Ranges {
		ships = collect(ships, sub, fleet, fleetName, sel)
	}
	return ships
}

func shipOf(id, name string, sky scene.Weather, peaks []scene.Peak, sel string) ship {
	sh := ship{id: id, name: name, sky: sky, selected: id != "" && id == sel, ships: 1}
	for _, p := range peaks {
		progress := p.Progress
		if math.IsNaN(progress) {
			progress = 0
		}
		sh.squads = append(sh.squads, squad{id: p.ID, name: p.Name, hit: p.ID, weather: p.Weather, team: p.Team, camps: p.Camps,
			needs: p.NeedsYou, progress: scene.Clamp(progress, 0, 1), dying: scene.Clamp(p.Dying, 0, 1), selected: p.ID == sel, units: 1})
	}
	return sh
}

// want is how wide a squadron would like to be: room for its name, its planes and its crew.
func (q squad) want() int {
	deck := 2 + len(q.team)
	for _, c := range q.camps {
		if !airborne(c) {
			deck += 4
		}
	}
	return min(maxSquad, max(minSquad, utf8.RuneCountInString(q.name)+6, deck))
}

func airborne(c scene.Camp) bool { return c.State == scene.Merged || c.Stage >= 6 }

// fit lays the carriers out on as few bands as fit them at the width they want, and when even every band is too
// narrow, folds squadrons together on each carrier, then carriers together, until they fit.
func fit(ships []ship, width, most int) ([]ship, int) {
	sized := func(ships []ship, wide bool) []ship {
		out := make([]ship, len(ships))
		for i, sh := range ships {
			sh.squads = append([]squad(nil), sh.squads...)
			for k := range sh.squads {
				sh.squads[k].w = minSquad
				if wide {
					sh.squads[k].w = sh.squads[k].want()
				}
			}
			out[i] = sh
		}
		return out
	}
	for b := 1; b <= most; b++ {
		if out := sized(ships, true); pack(out, width, b) {
			return out, b
		}
	}
	if out := sized(ships, false); pack(out, width, most) {
		return out, most
	}
	widest := 1
	for _, sh := range ships {
		widest = max(widest, len(sh.squads))
	}
	for k := widest - 1; k >= 1; k-- {
		folded := make([]ship, len(ships))
		for i, sh := range ships {
			folded[i] = sh
			folded[i].squads = foldSquads(sh, k)
		}
		if out := sized(folded, false); pack(out, width, most) {
			return out, most
		}
	}
	for n := len(ships) - 1; n >= 1; n-- {
		var merged []ship
		for i := range n {
			merged = append(merged, foldShips(ships[i*len(ships)/n:(i+1)*len(ships)/n]))
		}
		if out := sized(merged, false); pack(out, width, most) || n == 1 {
			pack(out, width, most)
			return out, most
		}
	}
	out := sized(ships, false)
	pack(out, width, most)
	return out, most
}

// pack puts each carrier on a band in order, reporting whether they all fit on bands bands.
func pack(ships []ship, width, bands int) bool {
	band, used := 0, 1
	for i := range ships {
		w := ships[i].width()
		if f := ships[i].fleet; f != "" && used > 1 && (i == 0 || ships[i-1].fleet != f) {
			run := w
			for k := i + 1; k < len(ships) && ships[k].fleet == f; k++ {
				run += gap + ships[k].width()
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
		ships[i].band = band
		used += w + gap
	}
	return true
}

func foldSquads(sh ship, k int) []squad {
	if len(sh.squads) <= k {
		return sh.squads
	}
	out := make([]squad, k)
	for i := range k {
		out[i] = merge(sh.squads[i*len(sh.squads)/k:(i+1)*len(sh.squads)/k], sh.id)
	}
	return out
}

// merge folds squadrons into one carrying their crews, planes, alerts and selection; a click on it selects hit.
func merge(qs []squad, hit string) squad {
	if len(qs) == 1 {
		return qs[0]
	}
	out := squad{id: qs[0].id + "+", hit: hit}
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
	out.name = strconv.Itoa(out.units) + " squadrons"
	return out
}

func foldShips(ships []ship) ship {
	if len(ships) == 1 {
		sh := ships[0]
		sh.squads = foldSquads(sh, 1)
		return sh
	}
	out := ship{id: "", fleet: ships[0].fleet, fleetName: ships[0].fleetName}
	var all []squad
	parts := make([]scene.Weather, 0, len(ships))
	for _, sh := range ships {
		all = append(all, sh.squads...)
		out.selected = out.selected || sh.selected || holds(sh)
		out.fleetSelected = out.fleetSelected || sh.fleetSelected
		out.ships += sh.ships
		parts = append(parts, sh.sky)
		if sh.fleet != out.fleet {
			out.fleet, out.fleetName = "", ""
		}
	}
	out.sky = scene.Combine(scene.Clouds, parts)
	out.name = strconv.Itoa(out.ships) + " carriers"
	if len(all) > 0 {
		q := merge(all, "")
		q.selected = false
		out.squads = []squad{q}
	}
	return out
}

func holds(sh ship) bool {
	for _, q := range sh.squads {
		if q.selected {
			return true
		}
	}
	return false
}

// place spreads each band's carriers across the width, giving squadrons what room is left up to what they want.
func (l *layout) place(width int) {
	for b := range l.bands {
		var on []int
		used, squads := 0, 0
		for i, sh := range l.ships {
			if sh.band == b {
				on = append(on, i)
				used += sh.width()
				squads += len(sh.squads)
			}
		}
		if len(on) == 0 {
			continue
		}
		spare := width - used - gap*(len(on)-1) - 2
		for spare > 0 && squads > 0 {
			grew := false
			for _, i := range on {
				for k := range l.ships[i].squads {
					q := &l.ships[i].squads[k]
					if spare > 0 && q.w < max(q.want(), maxSquad*2/3) {
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
			used += l.ships[i].width()
		}
		space := max(gap, (width-used)/(len(on)+1))
		x := max(1, (width-used-space*(len(on)-1))/2)
		for _, i := range on {
			sh := &l.ships[i]
			sh.x, sh.w = x, sh.width()
			at := x + 5
			for k := range sh.squads {
				sh.squads[k].x = at
				at += sh.squads[k].w
			}
			x += sh.w + space
		}
	}
	for i := 0; i < len(l.ships); {
		sh := l.ships[i]
		j := i + 1
		for j < len(l.ships) && sh.fleet != "" && l.ships[j].fleet == sh.fleet && l.ships[j].band == sh.band {
			j++
		}
		if sh.fleet != "" {
			last := l.ships[j-1]
			l.fleets = append(l.fleets, bracket{id: sh.fleet, name: sh.fleetName, selected: sh.fleetSelected, x0: sh.x, x1: last.x + last.w, band: sh.band})
		}
		i = j
	}
}
