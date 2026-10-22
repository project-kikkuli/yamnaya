package scene

import "time"

// Scene is everything the mountains show: ranges and loose peaks, the selected id, the local time that lights the
// sky, and whether the terminal is dark, which only sets contrast.
type Scene struct {
	Ranges   []Range
	Peaks    []Peak
	Selected string
	Dark     bool
	// Now is the local time; zero draws dusk.
	Now time.Time
}
