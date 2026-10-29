package kaiju

import "math"

// Easter eggs turn up rarely, each on its own clock of frames, a quarter second apart: a long watch finds them.
const (
	shakeEvery, shakeFor       = 400, 12
	bubbleEvery, bubbleFor     = 600, 40
	headEvery, headFor         = 720, 96
	singEvery, singFor         = 480, 32
	marqueeEvery, marqueeFor   = 900, 24
	headlineEvery, headlineFor = 1000, 12
)

func showing(frame, every, at, lasting int) (int, bool) {
	f := frame%every - at
	return f, f >= 0 && f < lasting
}

// shake is how far a handheld camera knocks the frame, in pixels: now and then, and often round the lanky beast.
func (p *painter) shake() (int, int) {
	_, on := showing(p.frame, shakeEvery, 200, shakeFor)
	if !on {
		for i := range p.l.monsters {
			if m := &p.l.monsters[i]; m.kind.shaky && m.dying == 0 && p.frame/3%9 == 0 {
				on = true
			}
		}
	}
	if !on {
		return 0, 0
	}
	return int(math.Round(2*unit(0x5ac, p.frame) - 1)), int(math.Round(2*unit(0x5ad, p.frame) - 1))
}

// eggs draws the rare sights on the set before the film prints it.
func (p *painter) eggs() {
	if f, on := showing(p.frame, bubbleEvery, 300, bubbleFor); on {
		p.destroyer(f)
	}
	if f, on := showing(p.frame, headEvery, 100, headFor); on {
		p.head(f)
	}
	if f, on := showing(p.frame, singEvery, 240, singFor); on {
		p.twins(f)
	}
	if _, on := showing(p.frame, headlineEvery, 700, headlineFor); on {
		p.paper()
	}
}

// destroyer is a glowing sphere sunk in the bay, streaming bubbles up to the surface.
func (p *painter) destroyer(f int) {
	cx := float64(p.w) * (.15 + .7*unit(0xd0, p.frame/bubbleEvery))
	cy := p.l.bottom - (p.l.bottom-p.l.quay)*.3
	r := 2 + .5*math.Sin(float64(f)*.8)
	for y := int(cy - r - 1); y <= int(cy+r+1); y++ {
		for x := int(cx - r - 1); x <= int(cx+r+1); x++ {
			if e := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy) / r; e < 1 {
				p.toward(x, y, 1, 1-e*e*.5)
				p.color(x, y, p.pal.breath, .4)
			}
		}
	}
	for n := range 8 {
		age := float64((f + n*5) % 20)
		x := cx + 3*math.Sin(age*.7+float64(n))
		y := cy - r - age*(cy-p.l.quay)/20
		p.toward(int(x), int(y), .95, .8)
	}
}

// head is the crowned head of a toppled statue, rolling down the quay from west to east.
func (p *painter) head(f int) {
	r := 3.0
	cx := -2*r + float64(f)/headFor*(float64(p.w)+4*r)
	cy := p.l.ground + (p.l.quay-p.l.ground)/2 - r + 2
	for y := int(cy - r); y <= int(cy+r); y++ {
		for x := int(cx - r); x <= int(cx+r); x++ {
			if math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy) < r {
				p.put(x, y, .5+.1*float64((x+y)&1))
			}
		}
	}
	spin := cx / r
	for n := range 7 {
		a := spin + float64(n)*2*math.Pi/7
		if math.Sin(a) > .3 {
			continue
		}
		for s := r; s < r+2.5; s++ {
			p.put(int(cx+math.Cos(a)*s), int(cy+math.Sin(a)*s), .6)
		}
	}
}

// mothOn is the first live moth on the board, or nil.
func (p *painter) mothOn() *monster {
	for i := range p.l.monsters {
		if m := &p.l.monsters[i]; m.kind == moth && m.dying == 0 {
			return m
		}
	}
	return nil
}

// twins are two tiny singers on the quay beside the moth, calling it.
func (p *painter) twins(f int) {
	m := p.mothOn()
	if m == nil {
		return
	}
	y := int(p.l.ground) + 1
	for _, x := range [2]int{int(m.x - m.w/2 - 6), int(m.x - m.w/2 - 4)} {
		sway := (f / 2) & 1
		p.put(x, y-3+sway, .95)
		p.put(x, y-2, .85)
		p.put(x, y-1, .85)
		p.put(x-1, y-1, .7)
		p.put(x+1, y-1, .7)
	}
}

// paper is the front page that flashes up in the sky with the news.
func (p *painter) paper() {
	cx, top := p.w/2, int(p.l.top)+2
	for y := top; y < top+6; y++ {
		for x := cx - 16; x < cx+16; x++ {
			v := .9
			if y == top+1 || y == top+5 {
				v = .6
			}
			p.put(x, y, v)
		}
	}
}

// eggWords writes what the rare sights say, over the print: the twins' song, the marquee, the headline.
func (p *painter) eggWords() {
	if _, on := showing(p.frame, singEvery, 240, singFor); on {
		if m := p.mothOn(); m != nil {
			y := row(p.l.ground) - 2
			p.deco(int(m.x-m.w/2)-5, y-p.frame/2%2, "♪", p.pal.label)
		}
	}
	if _, on := showing(p.frame, marqueeEvery, 450, marqueeFor); on {
		y := row(p.l.top) + 1
		text := "MONSTER ZERO"
		x := p.w/2 - 6
		p.label(text, p.pal.gold, [2]int{x, y}, [2]int{x, y + 1})
	}
	if _, on := showing(p.frame, headlineEvery, 700, headlineFor); on {
		y := row(p.l.top) + 2
		text := "GIANT MONSTER SIGHTED IN BAY"
		p.label(text, p.pal.shadow, [2]int{p.w/2 - 14, y})
	}
}
