package scene

// painter lays the scene down in pixels two to a cell, then glyphs on top.
type painter struct {
	th     theme
	l      layout
	w, p   int
	hz     int
	top    float64
	frame  int
	t      float64
	px     []rgb
	air    []rgb
	solid  []bool
	front  []bool
	cells  []glyph
	layers []layer
	far    []float64
	geo    []geom
	table  rect
}

type glyph struct {
	r   rune
	fg  rgb
	set bool
}

// layer is one depth of ridgeline: its height per column and which mount owns that column.
type layer struct {
	hm  []float64
	own []int
}

type geom struct{ height, half, x, y float64 }

type rect struct{ x0, y0, x1, y1 int }

func (r rect) has(x, y int) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

func newPainter(s Scene, width, height, frame int, table rect) *painter {
	th := day
	if s.Dark {
		th = dusk
	}
	p := &painter{th: th, w: width, p: height * 2, frame: frame, t: float64(frame), table: table}
	p.px = make([]rgb, p.w*p.p)
	p.solid = make([]bool, p.w*p.p)
	p.front = make([]bool, p.w*p.p)
	p.cells = make([]glyph, p.w*height)
	p.hz = p.p * 72 / 100
	if table.y1 > table.y0 {
		p.hz = int(clamp(float64(table.y0*2+2), float64(p.p)*.5, float64(p.p)*.8))
	}
	p.top = float64(p.hz) * .07
	p.l = plan(s, width)
	return p
}

func (p *painter) paint() {
	p.terrain()
	p.sky()
	p.halos()
	p.wisps()
	p.air = append([]rgb(nil), p.px...)
	p.ridge()
	for d := range p.layers {
		p.ground(d)
	}
	p.weather()
	p.shore()
	p.lake()
	p.pines()
	p.sprites()
}

func (p *painter) at(x, y int) rgb { return p.px[y*p.w+x] }

func (p *painter) set(x, y int, c rgb) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p {
		p.px[y*p.w+x] = c
	}
}

func (p *painter) blend(x, y int, c rgb, a float64) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p && a > 0 {
		p.px[y*p.w+x] = mix(p.px[y*p.w+x], c, a)
	}
}

func (p *painter) put(x, y int, r rune, fg rgb) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p/2 && !p.table.has(x, y) && !p.front[2*y*p.w+x] && !p.front[(2*y+1)*p.w+x] {
		p.cells[y*p.w+x] = glyph{r, fg, true}
	}
}

func (p *painter) label(x, y int, text string, fg rgb) {
	for _, r := range text {
		p.put(x, y, r, fg)
		x++
	}
}

// layerHeight is how tall the tallest mountain at a depth may stand, nearer ridges lower.
func (p *painter) layerHeight(d int) float64 {
	return (float64(p.hz) - p.top) * (1 - .46*float64(d)/float64(max(1, p.l.depth)))
}
