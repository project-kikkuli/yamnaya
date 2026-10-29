package kaiju

// sprite is a piece of art ready to scale: its pixels, a row at a time, and which of them border open air, where
// the searchlights catch it.
type sprite struct {
	w, h int
	px   []byte
	edge []bool
	// mx, my is its jaw, as a share of its width and height.
	mx, my float64
}

func spriteOf(art []string) *sprite {
	s := &sprite{h: len(art)}
	for _, r := range art {
		s.w = max(s.w, len(r))
	}
	s.px = make([]byte, s.w*s.h)
	for y, r := range art {
		for x := range s.w {
			s.px[y*s.w+x] = '.'
			if x < len(r) {
				s.px[y*s.w+x] = r[x]
			}
		}
	}
	s.edge = make([]bool, len(s.px))
	n, ex, ey, sx, sy := 0.0, 0.0, 0.0, 0.0, 0.0
	for y := range s.h {
		for x := range s.w {
			c := s.at(x, y)
			if c == '.' {
				continue
			}
			s.edge[y*s.w+x] = s.at(x-1, y) == '.' || s.at(x+1, y) == '.' || s.at(x, y-1) == '.' || s.at(x, y+1) == '.'
			switch c {
			case 'm':
				n, sx, sy = n+1, sx+float64(x), sy+float64(y)
			case 'e', 'l':
				if ex == 0 && ey == 0 {
					ex, ey = float64(x), float64(y)
				}
			}
		}
	}
	if n > 0 {
		s.mx, s.my = (sx/n+.5)/float64(s.w), (sy/n+.5)/float64(s.h)
	} else {
		s.mx, s.my = (ex+.5)/float64(s.w), (ey+1.5)/float64(s.h)
	}
	return s
}

func (s *sprite) at(x, y int) byte {
	if x < 0 || y < 0 || x >= s.w || y >= s.h {
		return '.'
	}
	return s.px[y*s.w+x]
}

// kind is one monster of the roster: its art, a second pose it beats into on alternate half seconds, and how it
// moves. Each unit keeps its kind, picked from its id.
type kind struct {
	name   string
	poses  [2]*sprite
	young  *sprite
	fly    float64
	sway   float64
	shaky  bool
	steel  bool
	gold   bool
	crawls bool
}

var (
	lizard  = &kind{name: "atomic lizard", poses: pose(lizardArt)}
	robot   = &kind{name: "giant robot", poses: pose(robotArt), steel: true}
	mecha   = &kind{name: "steel lizard", poses: pose(mechaArt), steel: true}
	moth    = &kind{name: "giant moth", poses: [2]*sprite{spriteOf(mothUp), spriteOf(mothDown)}, young: spriteOf(larvaArt), fly: .45}
	dragon  = &kind{name: "three-headed dragon", poses: pose(dragonArt), fly: .25, gold: true}
	turtle  = &kind{name: "giant turtle", poses: pose(turtleArt)}
	ptera   = &kind{name: "pteranodon", poses: pose(pteraArt), fly: .9}
	ape     = &kind{name: "giant ape", poses: pose(apeArt)}
	crab    = &kind{name: "giant crab", poses: pose(crabArt), crawls: true}
	serpent = &kind{name: "sea serpent", poses: pose(serpentArt), sway: 1.2}
	anky    = &kind{name: "ankylosaur", poses: pose(ankyArt)}
	lanky   = &kind{name: "lanky beast", poses: pose(lankyArt), shaky: true}
)

// roster is every kind of monster, in the order a unit's id picks from.
var roster = []*kind{lizard, robot, moth, dragon, turtle, ptera, ape, crab, serpent, anky, lanky, mecha, lizard}

func pose(art []string) [2]*sprite {
	s := spriteOf(art)
	return [2]*sprite{s, s}
}
