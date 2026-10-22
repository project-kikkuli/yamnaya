package scene

import (
	"os"

	"github.com/charmbracelet/colorprofile"
)

type colorProfile int

const (
	trueColor colorProfile = iota
	ansi256
	noColor
)

var (
	profile       = detectProfile()
	reducedMotion = os.Getenv("YM_REDUCED_MOTION") != "" && os.Getenv("YM_REDUCED_MOTION") != "0"
)

func detectProfile() colorProfile {
	switch {
	case os.Getenv("NO_COLOR") != "":
		return noColor
	case os.Getenv("COLORTERM") == "truecolor" || os.Getenv("COLORTERM") == "24bit":
		return trueColor
	case os.Getenv("TERM_PROGRAM") == "Apple_Terminal":
		return ansi256
	}
	return trueColor
}

// ColorProfile is the color depth the mountains are drawn in, for the terminal renderer to keep.
func ColorProfile() colorprofile.Profile {
	switch profile {
	case noColor:
		return colorprofile.Ascii
	case ansi256:
		return colorprofile.ANSI256
	}
	return colorprofile.TrueColor
}
