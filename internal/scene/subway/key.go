package subway

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// keyOf hashes everything the still map draws: the size, the table's box, the light to five minutes, the
// selection and every group and unit but how far a kill has played, which each frame draws.
func keyOf(s scene.Scene, width, height int, box scene.Rect) uint64 {
	h := hasher(14695981039346656037)
	h.int(width, height, box.X0, box.Y0, box.X1, box.Y1, int(scene.Colors))
	h.bool(s.Dark)
	if s.Now.IsZero() {
		h.int(-1)
	} else {
		h.int((s.Now.Hour()*60 + s.Now.Minute()) / 5)
	}
	h.str(s.Selected)
	s.Walk(func(p scene.Peak) {
		h.str(p.ID)
		h.str(p.Name)
		h.int(int(p.Weather), int(math.Float64bits(p.Progress)), len(p.Team), len(p.Camps))
		h.bool(p.NeedsYou)
		h.bool(p.Dying > 0)
		for _, c := range p.Team {
			h.int(int(c.Stance))
		}
		for _, c := range p.Camps {
			h.int(int(c.State), c.Stage)
		}
	}, func(r scene.Range) {
		h.str(r.ID)
		h.str(r.Name)
		h.int(int(r.Weather), len(r.Peaks), len(r.Ranges))
	})
	h.int(len(s.Peaks))
	return uint64(h)
}

type hasher uint64

func (h *hasher) byte(b byte) { *h = (*h ^ hasher(b)) * 1099511628211 }

func (h *hasher) str(s string) {
	for i := range len(s) {
		h.byte(s[i])
	}
	h.byte(0xff)
}

func (h *hasher) int(vs ...int) {
	for _, v := range vs {
		for k := range 8 {
			h.byte(byte(uint64(v) >> (8 * k)))
		}
	}
}

func (h *hasher) bool(b bool) {
	if b {
		h.byte(1)
	} else {
		h.byte(0)
	}
}
