package scene

import "os"

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
