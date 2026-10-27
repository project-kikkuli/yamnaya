package tui

import (
	"slices"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// fall is a unit a kill from the board takes off the scene: how it looked, the group it stood in, when the kill
// began, and the kill's keys; done once the kill reported back.
type fall struct {
	peak  scene.Peak
	group string
	at    time.Time
	keys  []string
	done  bool
}

// fell marks the units the confirmed kill of keys ends to fall: a goal, or a session the owner started once all
// its sessions die. The kill runs at once; the fall only plays on the scene.
func (m *Model) fell(keys []string, now time.Time) {
	marked := map[string]bool{}
	for _, k := range keys {
		marked[k] = true
	}
	ids := map[string]bool{}
	for _, k := range keys {
		if id, ok := strings.CutPrefix(k, "node:"); ok {
			ids[id] = true
			continue
		}
		id := m.board.ProjectOf(k)
		for _, p := range m.board.Projects {
			if p.Key != id {
				continue
			}
			all := true
			for _, s := range p.Sessions {
				all = all && marked[sessionKey(s)]
			}
			ids[id] = all
		}
	}
	s := m.sceneOf()
	var visit func(group string, peaks []scene.Peak, ranges []scene.Range)
	visit = func(group string, peaks []scene.Peak, ranges []scene.Range) {
		for _, p := range peaks {
			if ids[p.ID] {
				m.peaks.kills[p.ID] = fall{peak: p, group: group, at: now, keys: keys}
			}
		}
		for _, r := range ranges {
			visit(r.ID, r.Peaks, r.Ranges)
		}
	}
	visit("", s.Peaks, s.Ranges)
}

// falling is s with every falling unit in its kill animation, put back where it stood if the board already
// dropped it, and gone once the animation played.
func (m Model) falling(s scene.Scene, now time.Time) scene.Scene {
	for id, f := range m.peaks.kills {
		played := now.Sub(f.at)
		if played >= scene.KillTime {
			s.Peaks, s.Ranges = without(id, s.Peaks, s.Ranges)
			continue
		}
		dying := max(.01, float64(played)/float64(scene.KillTime))
		if !dye(id, dying, s.Peaks, s.Ranges) {
			p := f.peak
			p.Dying = dying
			s.Peaks, s.Ranges = into(f.group, p, s.Peaks, s.Ranges)
		}
	}
	return s
}

// settled drops the falls whose kill reported back and played out, once the board no longer shows the unit; a
// kill that failed puts its units back at once.
func (m *Model) settled(keys []string, failed bool) {
	for id, f := range m.peaks.kills {
		for _, k := range f.keys {
			if !slices.Contains(keys, k) {
				continue
			}
			if failed {
				delete(m.peaks.kills, id)
			} else {
				f.done = true
				m.peaks.kills[id] = f
			}
			break
		}
	}
}

// fallen forgets the falls that played out, reported back, and left the board.
func (m *Model) fallen(now time.Time) {
	var shown map[string]bool
	for id, f := range m.peaks.kills {
		if !f.done || now.Sub(f.at) < scene.KillTime {
			continue
		}
		if shown == nil {
			shown = map[string]bool{}
			m.boardScene().Walk(func(p scene.Peak) { shown[p.ID] = true }, func(scene.Range) {})
		}
		if !shown[id] {
			delete(m.peaks.kills, id)
		}
	}
}

func dye(id string, dying float64, peaks []scene.Peak, ranges []scene.Range) bool {
	for i := range peaks {
		if peaks[i].ID == id {
			peaks[i].Dying = dying
			return true
		}
	}
	for i := range ranges {
		if dye(id, dying, ranges[i].Peaks, ranges[i].Ranges) {
			return true
		}
	}
	return false
}

func without(id string, peaks []scene.Peak, ranges []scene.Range) ([]scene.Peak, []scene.Range) {
	var kept []scene.Peak
	for _, p := range peaks {
		if p.ID != id {
			kept = append(kept, p)
		}
	}
	for i := range ranges {
		ranges[i].Peaks, ranges[i].Ranges = without(id, ranges[i].Peaks, ranges[i].Ranges)
	}
	return kept, ranges
}

func into(group string, p scene.Peak, peaks []scene.Peak, ranges []scene.Range) ([]scene.Peak, []scene.Range) {
	if group == "" {
		return append(peaks, p), ranges
	}
	var put func(rs []scene.Range) bool
	put = func(rs []scene.Range) bool {
		for i := range rs {
			if rs[i].ID == group {
				rs[i].Peaks = append(rs[i].Peaks, p)
				return true
			}
			if put(rs[i].Ranges) {
				return true
			}
		}
		return false
	}
	if !put(ranges) {
		peaks = append(peaks, p)
	}
	return peaks, ranges
}
