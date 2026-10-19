package tui

type viewLine struct {
	text, place   string
	offset        int
	open          bool
	world, extent int
}
