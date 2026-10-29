package scene

import (
	"os"

	"github.com/charmbracelet/colorprofile"
)

// Depth is how many colors the terminal shows.
type Depth int

const (
	TrueColor Depth = iota
	ANSI256
	NoColor
)

var (
	// Colors is the terminal's color depth; themes draw shapes alone, no color, under NoColor.
	Colors = detectDepth()
	// ReducedMotion is YM_REDUCED_MOTION: every theme then draws frame 0 and nothing ticks.
	ReducedMotion = reducedMotion(os.Getenv("YM_REDUCED_MOTION"))
)

func reducedMotion(env string) bool { return env != "" && env != "0" }

func detectDepth() Depth {
	switch {
	case os.Getenv("NO_COLOR") != "":
		return NoColor
	case os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit":
		return TrueColor
	case os.Getenv("TERM_PROGRAM") == "Apple_Terminal":
		return ANSI256
	}
	return TrueColor
}

// ColorProfile is the color depth the scene is drawn in, for the terminal renderer to keep.
func ColorProfile() colorprofile.Profile {
	switch Colors {
	case NoColor:
		return colorprofile.Ascii
	case ANSI256:
		return colorprofile.ANSI256
	}
	return colorprofile.TrueColor
}
