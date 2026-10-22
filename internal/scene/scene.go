package scene

// Scene is everything the mountains show: ranges and loose peaks, the selected id, and the terminal's theme.
type Scene struct {
	Ranges   []Range
	Peaks    []Peak
	Selected string
	Dark     bool
}
