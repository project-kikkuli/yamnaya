package civilizations

import (
	"slices"
	"strconv"
	"sync"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	capital  = "⌂"
	crown    = "♛"
	ruin     = "Π"
	unitG    = "♞"
	envoy    = "♝"
	rester   = "♙"
	flag     = "⚑"
	wonder   = "♜"
	swords   = "⚔"
	rubbleA  = "▖"
	rubbleB  = "▝"
	razed    = "∩"
	forest   = "♣"
	mountain = "▲"
	fog      = "░"
	road     = "─"
	dashH    = "┄"
	dashV    = "┆"
)

var (
	oceanSeed = scene.SeedOf("civilizations ocean")
	isleSeed  = scene.SeedOf("civilizations isles")
	waveSeed  = scene.SeedOf("civilizations waves")
)

type kind uint8

const (
	ocean kind = iota
	ground
	decor
	content
)

type cell struct {
	fg, bg rgb
	g      string
	k      kind
}

// base is the map that holds still while the continents stand where they are: sea, coasts, islands and terrain.
type base struct {
	key   uint64
	cells []cell
	waves []int32
}

// still is everything on the map but what moves from frame to frame, for one scene at one size.
type still struct {
	key   uint64
	l     layout
	lt    light
	w, h  int
	cells []cell
	waves []int32
	// fires are ruined cells that flicker; edges the borders of territories in a golden age.
	fires, edges []int32
}

var cache struct {
	sync.Mutex
	base  *base
	still *still
}

func stillOf(s scene.Scene, width, height int) *still {
	key := sceneKey(s, width, height)
	cache.Lock()
	defer cache.Unlock()
	if st := cache.still; st != nil && st.key == key {
		return st
	}
	l := plan(s, width, height)
	lt := lightAt(s.Now, s.Dark)
	bk := baseKey(l, key0(s, width, height))
	b := cache.base
	if b == nil || b.key != bk {
		b = paintBase(l, lt, width, height, bk)
		cache.base = b
	}
	st := &still{key: key, l: l, lt: lt, w: width, h: height, cells: slices.Clone(b.cells), waves: b.waves}
	st.lay()
	cache.still = st
	return st
}

func (st *still) at(x, y int) *cell {
	if x < 0 || y < 0 || x >= st.w || y >= st.h {
		return nil
	}
	return &st.cells[y*st.w+x]
}

// paint draws decoration in fg over whatever is behind it, leaving that alone.
func (st *still) paint(x, y int, fg rgb, g string) {
	if c := st.at(x, y); c != nil {
		c.fg, c.g, c.k = fg, g, content
	}
}

// put draws a meaningful glyph read on whatever is behind it.
func (st *still) put(x, y int, fg rgb, g string) {
	if c := st.at(x, y); c != nil {
		c.fg, c.bg = scene.Readable(fg, c.bg)
		c.g, c.k = g, content
	}
}

// signal draws a glyph whose color is its meaning, on a cell darkened or lightened just enough to read.
func (st *still) signal(x, y int, fg rgb, g string) {
	if c := st.at(x, y); c != nil {
		c.fg, c.bg, c.g, c.k = fg, scene.Backing(fg, c.bg), g, content
	}
}

func (st *still) text(x, y, n int, s string, fg rgb, red bool) {
	drawn := 0
	for _, r := range s {
		if drawn >= n {
			return
		}
		if red {
			st.signal(x+drawn, y, fg, scene.Glyph(r))
		} else {
			st.put(x+drawn, y, fg, scene.Glyph(r))
		}
		drawn++
	}
}

func paintBase(l layout, lt light, w, h int, key uint64) *base {
	b := &base{key: key, cells: make([]cell, w*h)}
	for y := range h {
		for x := range w {
			d := scene.Fbm2(oceanSeed, float64(x)*.08, float64(y)*.2, 2)
			b.cells[y*w+x] = cell{bg: scene.Mix(lt.deep, lt.ocean, d*1.4-.2), g: " "}
		}
	}
	at := func(x, y int) *cell {
		if x < 0 || y < 0 || x >= w || y >= h {
			return nil
		}
		return &b.cells[y*w+x]
	}
	lay := func(x, y int, seed uint64) {
		c := at(x, y)
		if c == nil {
			return
		}
		c.bg, c.k = scene.Mix(lt.land, lt.plain, scene.Fbm2(seed, float64(x)*.2, float64(y)*.5, 2)*1.6-.3), ground
		switch f := scene.Fbm2(seed+1, float64(x)*.3, float64(y)*.7, 2); {
		case f > .66:
			c.g, c.fg, c.k = forest, lt.forest, decor
		case scene.Unit(seed, y*w+x) > .93:
			c.g, c.fg, c.k = mountain, lt.peak, decor
		}
	}
	near := make([]bool, w*h)
	mark := func(x0, y0, x1, y1 int) {
		for y := max(0, y0); y < min(h, y1); y++ {
			for x := max(0, x0); x < min(w, x1); x++ {
				near[y*w+x] = true
			}
		}
	}
	for _, n := range l.lands {
		r0, seed := l.row(n.band), scene.SeedOf("land "+n.id+n.name)
		mark(n.x-3, r0-1, n.x+n.w+3, r0+bandRows+1)
		for k := range 5 {
			left, right := indent(seed, k)
			for x := n.x + left; x < n.x+n.w-right; x++ {
				lay(x, r0+1+k, seed)
			}
		}
	}
	for _, e := range l.empires {
		r0 := l.row(e.band)
		mark(e.x0-2, r0-1, e.x1+3, r0+bandRows+1)
	}
	for y := range h {
		for x := range w {
			if !near[y*w+x] && scene.Fbm2(isleSeed, float64(x)*.16, float64(y)*.38, 3) > .8 {
				lay(x, y, isleSeed)
			}
		}
	}
	dist := make([]int8, w*h)
	for i := range dist {
		dist[i] = 9
	}
	for y := range h {
		for x := range w {
			if b.cells[y*w+x].k == ocean {
				continue
			}
			for dy := -2; dy <= 2; dy++ {
				for dx := -2; dx <= 2; dx++ {
					if c := at(x+dx, y+dy); c != nil {
						i := (y+dy)*w + x + dx
						dist[i] = min(dist[i], int8(max(abs(dx), abs(dy))))
					}
				}
			}
		}
	}
	for i := range b.cells {
		c := &b.cells[i]
		switch {
		case c.k != ocean:
			if c.k == decor {
				c.fg, c.bg = scene.Readable(c.fg, c.bg)
			}
		case dist[i] == 1:
			c.bg = scene.Mix(c.bg, lt.coast, .7)
		case dist[i] == 2:
			c.bg = scene.Mix(c.bg, lt.coast, .2)
		case scene.Unit(waveSeed, i) < .07:
			b.waves = append(b.waves, int32(i))
		}
	}
	return b
}

func abs(v int) int { return max(v, -v) }

// indent is how far row k of a continent's five pulls in from each end, so its coast is ragged.
func indent(seed uint64, k int) (left, right int) {
	most := [...]int{1, 0, 0, 0, 1}[k]
	return int(scene.Unit(seed, k) * float64(most+1)), int(scene.Unit(seed, k+5) * float64(most+1))
}

// lay lays the civilizations, continents and empires on the base map.
func (st *still) lay() {
	l := &st.l
	for _, e := range l.empires {
		st.empire(e)
	}
	for i := range l.lands {
		n := &l.lands[i]
		for k := range n.civs {
			st.territory(&n.civs[k], l.row(n.band))
		}
	}
	for i := range l.lands {
		n := &l.lands[i]
		for k := range n.civs {
			st.civ(&n.civs[k], l.row(n.band))
		}
		st.label(n, l.row(n.band))
	}
}

// inside reports whether x, y lies in the civilization's territory, which spreads from its capital as it progresses.
func inside(c *civ, seed uint64, r0, x, y int) bool {
	if x < c.x || x >= c.x+c.w || y < r0+1 || y > r0+5 {
		return false
	}
	cx, cy := float64(c.x)+2, float64(r0+3)
	rx, ry := 1.6+c.progress*float64(c.w-3), .7+c.progress*1.6
	dx, dy := (float64(x)-cx)/rx, (float64(y)-cy)/ry
	return dx*dx+dy*dy <= 1+.4*(scene.Noise2(seed, float64(x)*.7, float64(y)*.9)-.5)
}

func (st *still) territory(c *civ, r0 int) {
	lt, seed := st.lt, scene.SeedOf(c.id)
	tint := lt.tints[c.tint]
	if c.weather == scene.Eruption {
		tint = scene.Mix(lt.ash, lt.fire, .25)
	}
	tint = scene.Mix(tint, lt.ash, c.dying*1.6)
	fade := 1 - scene.Smooth(.5, 1, c.dying)
	for y := r0 + 1; y <= r0+5; y++ {
		for x := c.x; x < c.x+c.w; x++ {
			cl := st.at(x, y)
			if cl == nil {
				continue
			}
			i := int32(y*st.w + x)
			if !inside(c, seed, r0, x, y) {
				switch c.weather {
				case scene.Clouds:
					cl.fg, cl.bg, cl.g, cl.k = scene.Mix(cl.bg, lt.fog, .5), scene.Mix(cl.bg, lt.fog, .2), fog, ground
				case scene.Storm:
					cl.bg = scene.Mix(cl.bg, lt.pall, .45)
				}
				continue
			}
			edge := !inside(c, seed, r0, x-1, y) || !inside(c, seed, r0, x+1, y) || !inside(c, seed, r0, x, y-1) || !inside(c, seed, r0, x, y+1)
			k, t := .45, tint
			if edge {
				k = .8
				if c.weather == scene.Summit && c.dying == 0 {
					t, k = lt.gold, .4
					st.edges = append(st.edges, i)
				}
			}
			cl.bg = scene.Mix(cl.bg, t, k*fade)
			cl.g, cl.k = " ", ground
			if scene.Colors == scene.NoColor && edge {
				cl.g = "·"
			}
			u := scene.Unit(seed, int(i))
			switch {
			case c.weather == scene.Clouds:
				cl.fg, cl.bg, cl.g = scene.Mix(cl.bg, lt.fog, .45), scene.Mix(cl.bg, lt.fog, .15), fog
			case c.weather == scene.Storm:
				cl.bg = scene.Mix(cl.bg, lt.pall, .45)
			case c.weather == scene.Eruption && u < .4:
				cl.fg, cl.bg = scene.Readable(lt.ruin, cl.bg)
				cl.g, cl.k = rubbleOf(u), decor
				if u < .18 && c.dying == 0 {
					st.fires = append(st.fires, i)
				}
			}
			if c.dying > 0 && scene.Unit(seed, int(i)+7919) < .6*scene.Smooth(0, .5, c.dying)*(1-.8*scene.Smooth(.75, 1, c.dying)) {
				cl.fg, cl.bg = scene.Readable(lt.ruin, cl.bg)
				cl.g, cl.k = rubbleOf(u), decor
			}
		}
	}
}

// civ draws what stands in a civilization: its name, capital, road, cities, envoys and resting units.
func (st *still) civ(c *civ, r0 int) {
	lt, d := st.lt, c.dying
	r2, r3, r4 := r0+2, r0+3, r0+4
	if d <= .5 {
		for x := c.x + 3; x < c.x+c.w-1; x++ {
			if cl := st.at(x, r3); cl != nil && cl.k != content {
				st.paint(x, r3, scene.Mix(lt.road, lt.ash, d*2), road)
			}
		}
	}
	switch {
	case d > .35:
		st.put(c.x+2, r3, lt.ruin, ruin)
	case c.weather == scene.Summit:
		st.put(c.x+2, r3, lt.gold, crown)
	case c.weather == scene.Eruption:
		st.put(c.x+2, r3, lt.ruin, ruin)
	default:
		st.put(c.x+2, r3, lt.lit, capital)
	}
	camps := slices.Clone(c.camps)
	slices.SortStableFunc(camps, func(a, b scene.Camp) int { return urgency(a) - urgency(b) })
	room := (c.w - 4) / 2 * 2
	for i, cp := range camps[:min(len(camps), room)] {
		x, y := c.x+4+i/2*2, r2
		if i%2 == 1 {
			y = r4
		}
		if d > .2+.5*float64(i)/float64(max(1, len(camps))) {
			if d < .9 {
				st.put(x, y, lt.ruin, razed)
			}
			continue
		}
		switch g := cityGlyph(cp); cp.State {
		case scene.Merged:
			st.put(x, y, lt.gold, wonder)
		case scene.Green:
			st.put(x, y, lt.green, g)
		case scene.Failing:
			if cl := st.at(x, y); cl != nil {
				cl.bg = scene.Mix(cl.bg, scene.Mix(lt.fire, scene.Black, .7), .6)
			}
			st.signal(x, y, lt.red, g)
		default:
			st.put(x, y, lt.city, g)
		}
	}
	waiting, resting := 0, 0
	for _, m := range c.team {
		switch m.Stance {
		case scene.Waiting:
			waiting++
		case scene.Resting:
			resting++
		}
	}
	if d == 0 {
		for i := range min(waiting, 3) {
			st.signal(c.x+1, r3+[...]int{0, -1, 1}[i], lt.red, envoy)
		}
		if resting > 0 {
			st.put(c.x+2, r4, lt.rester, rester)
			if resting > 1 {
				st.put(c.x+3, r4, lt.rester, scene.Glyph(digit(resting)))
			}
		}
	}
	lead, pct := "", ""
	if d == 0 {
		pct = " " + strconv.Itoa(int(c.progress*100+.5)) + "%"
	}
	fg := scene.Mix(lt.label, lt.ash, d)
	if c.selected {
		lead, fg = "▾", scene.Mix(lt.label, extreme(lt.label), .6)
	}
	name := lead + clip(c.name, c.w-1-utf8.RuneCountInString(lead)-len(pct)) + pct
	switch {
	case c.needs && d == 0:
		st.text(c.x, r0+1, c.w-1, name, lt.red, true)
	case d < .85:
		st.text(c.x, r0+1, c.w-1, name, fg, false)
	}
}

// clip is s cut to n cells, ending in … when cut.
func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// urgency orders a crowded civilization's cities so the burning ones always show.
func urgency(c scene.Camp) int {
	return [...]int{scene.Failing: 0, scene.Green: 1, scene.Open: 2, scene.Merged: 3}[c.State]
}

// cityGlyph is a city as big as the gates its pull request passed.
func cityGlyph(c scene.Camp) string {
	switch {
	case c.Stage <= 1:
		return "∘"
	case c.Stage <= 3:
		return "▪"
	case c.Stage <= 5:
		return "■"
	}
	return "▣"
}

func digit(n int) rune {
	if n > 9 {
		return '+'
	}
	return rune('0' + n)
}

// extreme is white for a light glyph and black for a dark one, what a lit name moves toward.
func extreme(c rgb) rgb {
	if c.Luma() > .5 {
		return scene.White
	}
	return scene.Black
}

// label names the continent over its coast, with its weather, and walls a nested chief's in a dashed border.
func (st *still) label(n *land, r0 int) {
	lt := st.lt
	if n.nested {
		for y := r0 + 1; y <= r0+5; y++ {
			st.put(n.x-1, y, lt.border, dashV)
			st.put(n.x+n.w, y, lt.border, dashV)
		}
	}
	name := n.name
	if mark := skyMark(n.sky); mark != "" && name != "" {
		name += " " + mark
	}
	fg := lt.label
	if n.selected {
		name, fg = "▾ "+name, scene.Mix(lt.label, extreme(lt.label), .6)
	}
	if name == "" || n.empire != "" && n.id == n.empire {
		return
	}
	k := utf8.RuneCountInString(name)
	st.text(n.x+max(0, (n.w-k)/2), r0, n.w, name, fg, false)
}

func rubbleOf(u float64) string {
	if int(u*1000)%2 == 0 {
		return rubbleA
	}
	return rubbleB
}

func skyMark(w scene.Weather) string {
	return [...]string{scene.Clear: "", scene.Clouds: fog, scene.Storm: swords, scene.Eruption: rubbleA, scene.Summit: crown}[w]
}

// empire is a chief's dashed border round its continents, its name on the top edge over its own continent.
func (st *still) empire(e empire) {
	lt, r0, r6 := st.lt, st.l.row(e.band), st.l.row(e.band)+6
	for x := e.x0; x <= e.x1; x++ {
		top, bottom := dashH, dashH
		switch x {
		case e.x0:
			top, bottom = "╭", "╰"
		case e.x1:
			top, bottom = "╮", "╯"
		}
		st.put(x, r0, lt.border, top)
		st.put(x, r6, lt.border, bottom)
	}
	for y := r0 + 1; y < r6; y++ {
		st.put(e.x0, y, lt.border, dashV)
		st.put(e.x1, y, lt.border, dashV)
	}
	name, fg := e.name, lt.border
	if mark := skyMark(e.sky); mark != "" {
		name += " " + mark
	}
	if e.selected {
		name, fg = "▾ "+name, scene.Mix(lt.label, extreme(lt.label), .6)
	}
	name = " " + name + " "
	st.text(e.x0+1, r0, e.x1-e.x0-1, name, fg, false)
}

// frame is a frame of the map: the still picture with its waves, marching units, blinking flags, golden shimmer,
// war and fire laid over it.
func (st *still) frame(c *scene.Canvas, frame int, box scene.Rect) {
	set := func(x, y int, fg, bg rgb, g string) {
		if box.Has(x, y) {
			return
		}
		if scene.Colors == scene.NoColor {
			c.Plain(x, y, g)
			return
		}
		c.Set(x, y, fg, bg, g)
	}
	for y := range st.h {
		for x := range st.w {
			cl := &st.cells[y*st.w+x]
			set(x, y, cl.fg, cl.bg, cl.g)
		}
	}
	lt := st.lt
	phase := frame / 10
	for _, i := range st.waves {
		if v := scene.Spread(waveSeed, int(i)+phase*7919); v%4 == 0 {
			cl := &st.cells[i]
			g := "~"
			if v>>8&1 == 1 {
				g = "∼"
			}
			set(int(i)%st.w, int(i)/st.w, scene.Mix(cl.bg, lt.wave, .45), cl.bg, g)
		}
	}
	beat := frame / 4
	for _, i := range st.edges {
		x, y := int(i)%st.w, int(i)/st.w
		if (x+y+beat)%6 < 2 {
			cl := &st.cells[i]
			set(x, y, cl.fg, scene.Mix(cl.bg, lt.gold, .45), cl.g)
		}
	}
	for _, i := range st.fires {
		cl := &st.cells[i]
		u := scene.Unit(waveSeed, int(i)+frame*31)
		if u < .55 {
			f, b := scene.Readable(scene.Mix(lt.fire, lt.gold, u*1.5), cl.bg)
			g := "^"
			if u < .2 {
				g = "ʌ"
			}
			set(int(i)%st.w, int(i)/st.w, f, b, g)
		}
	}
	for _, n := range st.l.lands {
		r0 := st.l.row(n.band)
		for k := range n.civs {
			st.march(&n.civs[k], r0, beat, set)
		}
	}
}

// march moves a civilization's busy units along its road and blinks its envoy flag when it needs you.
func (st *still) march(c *civ, r0, beat int, set func(x, y int, fg, bg rgb, g string)) {
	if c.dying > 0 {
		return
	}
	lt, r3 := st.lt, r0+3
	if c.weather == scene.Storm {
		x := c.x + c.w - 2 - beat%2
		if cl := st.at(x, r3); cl != nil {
			f, b := scene.Readable(lt.steel, cl.bg)
			set(x, r3, f, b, swords)
		}
	}
	busy := 0
	for _, m := range c.team {
		if m.Stance == scene.Climbing {
			busy++
		}
	}
	if span := c.w - 4; span > 0 && busy > 0 {
		shown := min(busy, span)
		for k := range shown {
			x := c.x + 3 + (k*span/shown+beat)%span
			if cl := st.at(x, r3); cl != nil {
				set(x, r3, lt.busy, scene.Backing(lt.busy, cl.bg), unitG)
			}
		}
	}
	if c.needs && beat%2 == 0 {
		if cl := st.at(c.x+2, r0+2); cl != nil {
			set(c.x+2, r0+2, lt.red, scene.Backing(lt.red, cl.bg), flag)
		}
	}
}

type hasher uint64

func (h *hasher) n(v int) { *h = hasher(scene.Spread(uint64(*h), v)) }

func (h *hasher) s(s string) {
	h.n(len(s))
	for i := 0; i < len(s); i++ {
		h.n(int(s[i]))
	}
}

func (h *hasher) f(v float64) { h.n(int(v * 1e6)) }

func (h *hasher) peak(p scene.Peak) {
	h.s(p.ID)
	h.s(p.Name)
	h.n(int(p.Weather))
	h.f(p.Progress)
	h.f(p.Dying)
	if p.NeedsYou {
		h.n(1)
	}
	h.n(len(p.Team))
	for _, m := range p.Team {
		h.n(int(m.Stance))
	}
	h.n(len(p.Camps))
	for _, c := range p.Camps {
		h.n(int(c.State)*8 + c.Stage)
	}
}

func (h *hasher) rng(r scene.Range) {
	h.s(r.ID)
	h.s(r.Name)
	h.n(int(r.Weather))
	h.n(len(r.Peaks))
	for _, p := range r.Peaks {
		h.peak(p)
	}
	h.n(len(r.Ranges))
	for _, sub := range r.Ranges {
		h.rng(sub)
	}
}

// key0 is what every picture of the scene at this size shares: the size, the terminal and the light.
func key0(s scene.Scene, width, height int) uint64 {
	h := hasher(7)
	h.n(width)
	h.n(height)
	h.n(int(scene.Colors))
	if s.Dark {
		h.n(1)
	}
	if !s.Now.IsZero() {
		h.n(int(s.Now.Unix() / int64(lightStep.Seconds())))
	}
	return uint64(h)
}

func sceneKey(s scene.Scene, width, height int) uint64 {
	h := hasher(key0(s, width, height))
	h.s(s.Selected)
	h.n(len(s.Ranges))
	for _, r := range s.Ranges {
		h.rng(r)
	}
	h.n(len(s.Peaks))
	for _, p := range s.Peaks {
		h.peak(p)
	}
	return uint64(h)
}

// baseKey is what shapes the base map: the light and size, and where each continent and empire stands.
func baseKey(l layout, k uint64) uint64 {
	h := hasher(k)
	h.n(l.top)
	h.n(l.step)
	for _, n := range l.lands {
		h.s(n.id)
		h.s(n.name)
		h.n(n.x)
		h.n(n.w)
		h.n(n.band)
	}
	for _, e := range l.empires {
		h.n(e.x0)
		h.n(e.x1)
		h.n(e.band)
	}
	return uint64(h)
}
