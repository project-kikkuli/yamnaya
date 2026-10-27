// Package scene is the board as a picture: the theme-independent model of groups, units, agents and pull
// requests, the canvas a theme paints it on, and the registry of themes. THEMES.md says how to add one.
package scene

import "time"

// Scene is everything a theme shows: groups and loose units, the selected id, the local time that lights the
// sky, and whether the terminal is dark, which only sets contrast.
type Scene struct {
	Ranges   []Range
	Peaks    []Peak
	Selected string
	Dark     bool
	// Now is the local time; zero draws dusk.
	Now time.Time
	// Theme names the theme that draws it; "" or an unknown name is the default.
	Theme string
}

// Killing reports whether a kill animation plays anywhere in the scene.
func (s Scene) Killing() bool {
	found := false
	s.Walk(func(p Peak) { found = found || p.Dying > 0 }, func(Range) {})
	return found
}

// Walk visits every unit and group, nested ones included.
func (s Scene) Walk(unit func(Peak), group func(Range)) {
	var visit func(r Range)
	visit = func(r Range) {
		group(r)
		for _, p := range r.Peaks {
			unit(p)
		}
		for _, sub := range r.Ranges {
			visit(sub)
		}
	}
	for _, r := range s.Ranges {
		visit(r)
	}
	for _, p := range s.Peaks {
		unit(p)
	}
}
