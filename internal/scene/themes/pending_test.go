package themes

import (
	"strings"
	"testing"
)

// pending is, for each check, the themes that fail it until their fixers change them, and why. A listed theme's
// failures are logged, not failed, and once it passes the check fails until its entry is removed. Temporary: the
// map empties as themes are fixed.
var pending = map[string]map[string]string{
	"TestRedMarksOnlyWaitingAgentsNeedsYouAndFailingCI": {
		"mountains": "rock and lava pixels at hues 0 to 19",
		"forest":    "bark and soil pixels at hues 15 to 19",
		"islands":   "an orange pixel at hue 19.8",
		"skyline":   "brick pixels at hues 10 to 19",
	},
	"TestRedOnABusyBoardIsOnlyItsSignals": {
		"mountains": "rock and lava pixels at hues 0 to 19, 18% of the board at 80x24",
		"anthill":   "salmon soil at hue 16",
		"carrier":   "a brown deck at hue 19 at dusk",
		"forest":    "bark and soil pixels at hues 15 to 19",
		"moai":      "stone at hue 19",
		"skyline":   "brick pixels at hues 15 to 19",
	},
	"TestLabelsKeepApartAndNamesStayTellable": {
		"mountains":     "pull request numbers touch camps, agent names touch numbers, names cut to \"ship\"",
		"anthill":       "agent names cut to \"agent\"",
		"carrier":       "unit names cut to \"ship un\"",
		"civilizations": "unit names cut to \"ship uni\", goals to \"fix login\"",
		"forest":        "progress touches agent names, numbers touch nests, names cut to \"fix l\"",
		"islands":       "pull request numbers touch their glyphs, names cut to \"ship unit\"",
		"moai":          "goal names cut to \"fix login\"",
		"pyramids":      "pull request numbers touch their blocks, names cut",
		"skyline":       "names touch workers and stars, unit and agent names cut",
		"stocks":        "unit names cut to \"sh\" and \"ship un\"",
		"subway":        "line names cut to \"ship\"",
	},
}

// failer reports a theme's failures of the running check, which pending may hold off.
func failer(t *testing.T, theme string) func(format string, args ...any) {
	t.Helper()
	check := strings.Split(t.Name(), "/")[0]
	why, held := pending[check][theme]
	failed := false
	if held {
		t.Cleanup(func() {
			if !failed {
				t.Errorf("%s passes %s now: remove it from pending", theme, check)
			}
		})
	}
	return func(format string, args ...any) {
		t.Helper()
		failed = true
		if held {
			t.Logf("pending (%s): "+format, append([]any{why}, args...)...)
			return
		}
		t.Errorf(format, args...)
	}
}
