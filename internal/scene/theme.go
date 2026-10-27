package scene

import (
	"slices"
	"strings"
	"sync"
)

// Theme draws a scene. It paints the same model every theme gets, so it is free in how it looks but must show
// every Signal, and its Legend says how; the help screen is built from that legend.
type Theme interface {
	// Name is what ym theme and the switch key call it: one lowercase word.
	Name() string
	// Paint draws frame of s filling width x height, leaving box, where the table floats, to the table. The
	// same arguments always give the same canvas. Frames are Step apart.
	Paint(s Scene, width, height, frame int, box Rect) *Canvas
	// Hit is the id of the unit or group drawn at x, y in frame 0, its name and agents included; "" elsewhere.
	Hit(s Scene, width, height int, box Rect, x, y int) string
	// Legend is how the theme shows each Signal at the scene's time of day, and what it draws only to decorate.
	Legend(s Scene) Legend
}

// Legend is a theme's half of the help: Entries say how each Signal looks, Decoration what has no meaning.
type Legend struct {
	Entries    []Entry
	Decoration []string
}

// Entry is one Signal as a theme draws it: Glyph in Color, "" for the terminal's own, looking as Look says.
type Entry struct {
	Signal Signal
	Glyph  string
	Color  string
	Look   string
}

// Signal is something on the board every theme shows.
type Signal int

const (
	SigGroup Signal = iota
	SigUnit
	SigProgress
	SigBusy
	SigWaiting
	SigResting
	SigPR
	SigPRGreen
	SigPRRed
	SigMerged
	SigDone
	SigNeedsYou
	SigSelected
	SigClear
	SigClouds
	SigStorm
	SigEruption
	SigSummit
	SigKill
	signals
)

// Signals is every Signal in the order help lists them.
func Signals() []Signal {
	out := make([]Signal, signals)
	for i := range out {
		out[i] = Signal(i)
	}
	return out
}

// IsWeather reports whether the signal is a unit's weather, listed apart in help.
func (g Signal) IsWeather() bool { return g >= SigClear && g <= SigSummit }

func (g Signal) String() string {
	return [...]string{"group", "unit", "progress", "busy", "waiting", "resting", "pr", "pr green", "pr red", "merged", "done",
		"needs you", "selected", "Clear", "Clouds", "Storm", "Eruption", "Summit", "kill"}[g]
}

// When is what on the board the signal stands for, the same in every theme.
func (g Signal) When() string {
	return [...]string{
		SigGroup:    "a repository; a chief groups the goals and chiefs under it, nesting",
		SigUnit:     "a goal, or a session you started",
		SigProgress: "how far along the unit is (see Percentages)",
		SigBusy:     "an agent at work",
		SigWaiting:  "an agent stopped and waiting on you; always red",
		SigResting:  "idle teammates that stay with the unit",
		SigPR:       "a pull request; its stage is how many of its 6 gates passed (branch, commits, PR, CI, threads, merged)",
		SigPRGreen:  "a pull request whose CI passed",
		SigPRRed:    "a pull request whose CI fails",
		SigMerged:   "a merged pull request",
		SigDone:     "a goal done, or every pull request of a session merged",
		SigNeedsYou: "something on the unit waits on you",
		SigSelected: "the unit or group of the selected row",
		SigClear:    "a goal that is moving or verifying; a session with an agent at work or waiting, or with both a teammate and a pull request. It outranks Summit while an agent is busy",
		SigClouds:   "a goal that is quiet (nudged, or nothing moved for 30 minutes) or adrift; a session with no working agent, or with pull requests but no teammate",
		SigStorm:    "a stuck goal, or a pull request with failing CI",
		SigEruption: "the manager's session died and has not come back",
		SigSummit:   "a goal done, or a session whose pull requests are all merged with no one at work; a group only when all its members are",
		SigKill:     "a unit you killed, for at most 1.5s before it leaves; the kill itself never waits for it",
	}[g]
}

var registry struct {
	sync.Mutex
	themes []Theme
}

// Default is the theme a scene with no theme, or an unknown one, is drawn in.
const Default = "mountains"

// Register adds a theme; each theme package calls it from init.
func Register(t Theme) {
	registry.Lock()
	defer registry.Unlock()
	if slices.ContainsFunc(registry.themes, func(o Theme) bool { return o.Name() == t.Name() }) {
		panic("scene: theme " + t.Name() + " registered twice")
	}
	registry.themes = append(registry.themes, t)
	slices.SortFunc(registry.themes, func(a, b Theme) int {
		switch {
		case a.Name() == Default:
			return -1
		case b.Name() == Default:
			return 1
		}
		return strings.Compare(a.Name(), b.Name())
	})
}

// Themes is every registered theme, the default first and then by name.
func Themes() []Theme {
	registry.Lock()
	defer registry.Unlock()
	return slices.Clone(registry.themes)
}

// Lookup is the theme called name, or the default.
func Lookup(name string) Theme {
	all := Themes()
	for _, t := range all {
		if t.Name() == name {
			return t
		}
	}
	for _, t := range all {
		if t.Name() == Default {
			return t
		}
	}
	panic("scene: no themes registered; import internal/scene/themes")
}

// Next is the theme after name in Themes, wrapping around.
func Next(name string) string {
	all := Themes()
	cur := Lookup(name).Name()
	for i, t := range all {
		if t.Name() == cur {
			return all[(i+1)%len(all)].Name()
		}
	}
	return Default
}
