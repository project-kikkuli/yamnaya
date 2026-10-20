package tui

type alpineEvent struct {
	kind, mountain, team, artifact string
	frame                          uint64
	people                         int
}

func (e alpineEvent) life() uint64 {
	if e.kind == "storm" {
		return 75
	}
	return 45
}
