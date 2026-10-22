package scene

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

func TestMain(m *testing.M) {
	profile, reducedMotion = trueColor, false
	m.Run()
}

var sizes = []struct{ w, h int }{{80, 24}, {100, 30}, {160, 48}, {260, 70}, {40, 12}, {20, 6}, {1, 1}}

func TestFrameFillsExactlyWidthByHeight(t *testing.T) {
	for _, size := range sizes {
		for _, table := range []string{"", board(5, 0), board(80, 0), strings.Repeat("x", 400)} {
			for _, dark := range []bool{true, false} {
				name := fmt.Sprintf("%dx%d/%d/dark=%v", size.w, size.h, len(table), dark)
				t.Run(name, func(t *testing.T) {
					s := caucasus(dark)
					rows := strings.Split(Frame(s, size.w, size.h, 7, table), "\n")
					if len(rows) != size.h {
						t.Fatalf("rows = %d, want %d", len(rows), size.h)
					}
					for i, row := range rows {
						if got := ansi.StringWidth(row); got != size.w {
							t.Fatalf("row %d width = %d, want %d", i, got, size.w)
						}
					}
				})
			}
		}
	}
}

func TestFrameOfNothingIsEmpty(t *testing.T) {
	for _, size := range []struct{ w, h int }{{0, 0}, {0, 10}, {10, 0}} {
		if got := Frame(caucasus(true), size.w, size.h, 0, "x"); got != "" {
			t.Errorf("%dx%d gave %q", size.w, size.h, got)
		}
	}
}

func TestTableTextSurvivesIntact(t *testing.T) {
	for _, size := range sizes[:5] {
		for _, frame := range []int{0, 13, 999} {
			for _, scene := range []Scene{caucasus(true), caucasus(false), {}} {
				table := board(min(10, size.h-2), 0)
				out := strings.Split(ansi.Strip(Frame(scene, size.w, size.h, frame, table)), "\n")
				for _, line := range strings.Split(strings.TrimRight(table, "\n"), "\n") {
					want := strings.TrimRight(ansi.Strip(ansi.Truncate(line, size.w, "")), " ")
					found := false
					for _, row := range out {
						if strings.Contains(row, want) {
							found = true
						}
					}
					if !found {
						t.Fatalf("%dx%d frame %d: table line %q missing", size.w, size.h, frame, want)
					}
				}
			}
		}
	}
}

func TestTableKeepsItsOwnStyling(t *testing.T) {
	table := "\x1b[31mred alert\x1b[0m"
	out := Frame(caucasus(true), 80, 24, 3, table)
	if !strings.Contains(out, reset+" "+table) {
		t.Fatalf("table styling not passed through after a reset")
	}
}

func TestSameFrameSamePicture(t *testing.T) {
	a := Frame(caucasus(true), 120, 40, 42, board(6, 0))
	b := Frame(caucasus(true), 120, 40, 42, board(6, 0))
	if a != b {
		t.Fatal("same inputs drew different pictures")
	}
	if a == Frame(caucasus(true), 120, 40, 46, board(6, 0)) {
		t.Fatal("the next frame did not move")
	}
}

func TestReducedMotionHoldsStill(t *testing.T) {
	reducedMotion = true
	defer func() { reducedMotion = false }()
	if Frame(caucasus(true), 120, 40, 1, "") != Frame(caucasus(true), 120, 40, 500, "") {
		t.Fatal("frames differ under reduced motion")
	}
}

// region is the average color and glyphs in the cells around one peak's summit.
func region(t *testing.T, w Weather, dark bool) (rgb, string) {
	t.Helper()
	s := Scene{Dark: dark, Peaks: []Peak{{ID: "a", Weather: Clear}, {ID: "b", Weather: w, Team: team(2), Progress: .5}, {ID: "c", Weather: Clear}}}
	const width, height = 120, 40
	grid := cells(t, Frame(s, width, height, 5, ""))
	p := newPainter(s, width, height, 5, rect{})
	p.terrain()
	g := p.geo[1]
	var sum rgb
	var glyphs strings.Builder
	n := 0.0
	for y := max(0, int(g.y)/2); y < min(height, int(g.y+g.height*.8)/2); y++ {
		for x := max(0, int(g.x-g.half*.6)); x < min(width, int(g.x+g.half*.6)); x++ {
			c := grid[y][x]
			sum = rgb{sum.r + float64(c.bg.R), sum.g + float64(c.bg.G), sum.b + float64(c.bg.B)}
			n++
			if c.r != ' ' && c.r != '▀' {
				glyphs.WriteRune(c.r)
			}
		}
	}
	return sum.scale(1 / n / 255), glyphs.String()
}

func TestEachWeatherShowsAtItsPeak(t *testing.T) {
	for _, dark := range []bool{true, false} {
		clear, clearGlyphs := region(t, Clear, dark)
		storm, stormGlyphs := region(t, Storm, dark)
		erupt, _ := region(t, Eruption, dark)
		summit, summitGlyphs := region(t, Summit, dark)
		clouds, _ := region(t, Clouds, dark)
		if storm.luma() >= clear.luma() {
			t.Errorf("dark=%v: storm %.3f not darker than clear %.3f", dark, storm.luma(), clear.luma())
		}
		if !strings.ContainsRune(stormGlyphs, '╱') {
			t.Errorf("dark=%v: no rain in the storm", dark)
		}
		if erupt.r-erupt.b <= clear.r-clear.b+.05 {
			t.Errorf("dark=%v: eruption not redder: %+v vs %+v", dark, erupt, clear)
		}
		if !strings.ContainsRune(summitGlyphs, '⚑') || strings.ContainsRune(clearGlyphs, '⚑') {
			t.Errorf("dark=%v: summit flag only on the summited peak: %q vs %q", dark, summitGlyphs, clearGlyphs)
		}
		if summit.r <= clear.r {
			t.Errorf("dark=%v: summit not warmer than clear", dark)
		}
		if clouds == clear {
			t.Errorf("dark=%v: clouds look like clear sky", dark)
		}
		if !strings.ContainsAny(clearGlyphs, "λƛ") {
			t.Errorf("dark=%v: no climbers on the clear peak", dark)
		}
	}
}

func TestSelectedPeakIsNamedEvenDeepInNestedRanges(t *testing.T) {
	s := Scene{Dark: true, Selected: "deep", Ranges: []Range{{ID: "outer", Name: "outer", Peaks: []Peak{{ID: "o1"}, {ID: "o2"}}, Ranges: []Range{
		{ID: "mid", Peaks: []Peak{{ID: "m1"}}, Ranges: []Range{{ID: "inner", Peaks: []Peak{{ID: "deep", Name: "Dykh-Tau"}, {ID: "i2"}}}}},
	}}}}
	out := ansi.Strip(Frame(s, 160, 48, 0, ""))
	if !strings.Contains(out, "Dykh-Tau") {
		t.Fatal("selected nested peak not named")
	}
	flat := Scene{Dark: true, Selected: "deep", Peaks: []Peak{{ID: "o1"}, {ID: "o2"}, {ID: "m1"}, {ID: "deep", Name: "Dykh-Tau"}, {ID: "i2"}}}
	if Frame(s, 160, 48, 0, "") == Frame(flat, 160, 48, 0, "") {
		t.Fatal("nesting drew the same as a flat row of peaks")
	}
	l := plan(s, 160)
	if l.depth != 2 || len(l.massifs) != 3 {
		t.Fatalf("depth %d with %d massifs, want 3 nested ranges at depth 2", l.depth, len(l.massifs))
	}
}

func TestCrowdsMergeAndKeepTheSelection(t *testing.T) {
	s := Scene{Dark: true, Selected: "p250"}
	for i := range 500 {
		s.Peaks = append(s.Peaks, Peak{ID: fmt.Sprint("p", i), Name: fmt.Sprint("peak ", i), Team: team(1)})
	}
	l := plan(s, 120)
	if len(l.mounts) > 120/int(minPeak) {
		t.Fatalf("%d mounts drawn on 120 columns", len(l.mounts))
	}
	selected, climbers := 0, 0
	for _, m := range l.mounts {
		if m.selected {
			selected++
		}
		climbers += len(m.team)
	}
	if selected != 1 || climbers != 500 {
		t.Fatalf("selected %d mounts, %d climbers carried", selected, climbers)
	}
}

func TestFiveHundredPeaksDrawQuickly(t *testing.T) {
	if raceDetector {
		t.Skip("the race detector slows drawing several times over")
	}
	s := Scene{Dark: true}
	for r := range 25 {
		rg := Range{ID: fmt.Sprint("r", r)}
		for k := range 20 {
			rg.Peaks = append(rg.Peaks, Peak{ID: fmt.Sprint(r, "-", k), Weather: Weather(k % 5), Team: team(k % 3), Progress: .5})
		}
		s.Ranges = append(s.Ranges, rg)
	}
	Frame(s, 300, 90, 0, board(20, 0))
	fastest := time.Hour
	for f := range 5 {
		start := time.Now()
		Frame(s, 300, 90, f, board(20, 0))
		fastest = min(fastest, time.Since(start))
	}
	if per := fastest; per > 80*time.Millisecond {
		t.Fatalf("a 300x90 frame of 500 peaks took %v", per)
	}
}

func TestWeatherOfARangeIsItsWorstStormOrAllDone(t *testing.T) {
	cases := []struct {
		own   Weather
		parts []Weather
		want  Weather
	}{
		{Clear, []Weather{Clear, Storm, Clouds}, Storm},
		{Clear, []Weather{Storm, Eruption}, Eruption},
		{Clear, []Weather{Summit, Summit}, Summit},
		{Clear, []Weather{Summit, Clear}, Clear},
		{Clear, []Weather{Clouds, Clouds}, Clouds},
		{Storm, []Weather{Summit}, Storm},
		{Summit, nil, Summit},
		{Clear, nil, Clear},
	}
	for _, c := range cases {
		if got := combine(c.own, c.parts); got != c.want {
			t.Errorf("combine(%v, %v) = %v, want %v", c.own, c.parts, got, c.want)
		}
	}
}

func TestAnyPeakNameKeepsTheFrameShape(t *testing.T) {
	names := []string{"日本語の山の名前です", "🏔️⛰️🗻", "é́x", "\x1b[31mred\x1b[0m", "a\tb\rc", strings.Repeat("宽", 200), ""}
	table := "日本語テーブル\n🎉 party row\n\x1b[31mred\x1b[0m row"
	for _, name := range names {
		for _, size := range sizes {
			s := Scene{Dark: true, Selected: "b", Ranges: []Range{{ID: "r", Name: name, Peaks: []Peak{{ID: "a", Name: name, Team: team(3)}}}}}
			for i := range 40 {
				s.Peaks = append(s.Peaks, Peak{ID: fmt.Sprint(i), Name: name, Weather: Weather(i % 5), Team: team(i % 4)})
			}
			s.Peaks[1].ID = "b"
			for _, sel := range []string{"b", "r"} {
				s.Selected = sel
				out := Frame(s, size.w, size.h, 3, table)
				rows := strings.Split(out, "\n")
				for i, row := range rows {
					if got := ansi.StringWidth(row); got != size.w {
						t.Fatalf("name %q at %dx%d: row %d width %d", name, size.w, size.h, i, got)
					}
				}
				if size.h >= 3 && !strings.Contains(ansi.Strip(out), ansi.Strip(ansi.Truncate("🎉 party row", size.w, ""))) {
					t.Fatalf("name %q at %dx%d: table row lost", name, size.w, size.h)
				}
			}
		}
	}
}

func TestProgressOutOfRangeDrawsAsItsNearestEnd(t *testing.T) {
	at := func(progress float64) string {
		return Frame(Scene{Dark: true, Peaks: []Peak{{ID: "a", Team: team(4), Progress: progress}}}, 80, 24, 0, "")
	}
	for _, c := range []struct{ odd, end float64 }{{math.NaN(), 0}, {-3, 0}, {math.Inf(-1), 0}, {7, 1}, {math.Inf(1), 1}} {
		if at(c.odd) != at(c.end) {
			t.Errorf("progress %v drew differently from %v", c.odd, c.end)
		}
	}
}

func work() Scene {
	return Scene{Dark: true, Ranges: []Range{{ID: "repo:august", Name: "august", Peaks: []Peak{
		{ID: "allergies", Name: "Arbiter allergies rollout", Progress: .6, NeedsYou: true,
			Team:  []Climber{{Name: "hchb-readback", Stance: Climbing}, {Name: "fix-18581", Stance: Waiting}, {Name: "free-disk", Stance: Resting}, {Name: "admit", Stance: Resting}},
			Camps: []Camp{{Label: "#18633", State: Failing, Stage: 3}, {Label: "#18757", State: Green, Stage: 4}, {Label: "#18547", State: Merged, Stage: 6}, {Label: "#18581", State: Merged, Stage: 6}}},
		{ID: "workstations", Name: "Workstations concept", Weather: Clouds},
	}}}}
}

func TestWorkReadsFromThePicture(t *testing.T) {
	for _, dark := range []bool{true, false} {
		s := work()
		s.Dark = dark
		out := ansi.Strip(Frame(s, 174, 72, 0, board(20, 0)))
		for _, want := range []string{"Arbiter allergies rollout 60%", "Workstations concept", "august", "hchb-readback", "fix-18581", "2 resting", "#18633", "#18757", "2 merged"} {
			if !strings.Contains(out, want) {
				t.Errorf("dark=%v: %q not drawn", dark, want)
			}
		}
		if strings.Contains(out, "#18547") {
			t.Errorf("dark=%v: merged pull requests not folded into one flag", dark)
		}
		if !strings.ContainsAny(out, "✸✦") {
			t.Errorf("dark=%v: no flare over the work that needs the owner", dark)
		}
	}
}

func TestPaceFollowsWhatMoves(t *testing.T) {
	quiet := Scene{Peaks: []Peak{{ID: "a", Weather: Clouds, Team: []Climber{{Name: "r", Stance: Resting}}}}}
	cases := []struct {
		name string
		s    Scene
		want time.Duration
	}{
		{"quiet", quiet, 0},
		{"climbing", Scene{Peaks: []Peak{{ID: "a", Team: team(1)}}}, time.Second},
		{"needs you", Scene{Peaks: []Peak{{ID: "a", NeedsYou: true}}}, time.Second},
		{"storm", Scene{Ranges: []Range{{ID: "r", Peaks: []Peak{{ID: "a", Weather: Storm}}}}}, time.Second},
		{"eruption", Scene{Peaks: []Peak{{ID: "a", Weather: Eruption}}}, Step},
	}
	for _, c := range cases {
		if got := Pace(c.s); got != c.want {
			t.Errorf("%s: Pace = %v, want %v", c.name, got, c.want)
		}
	}
	for f := range 12 {
		if Frame(quiet, 120, 40, f, board(5, 0)) != Frame(quiet, 120, 40, 0, board(5, 0)) {
			t.Fatalf("a scene that does not move changed at frame %d", f)
		}
	}
}

func TestHitFindsThePeakUnderACell(t *testing.T) {
	s, table := work(), board(20, 0)
	grid := cells(t, Frame(s, 174, 72, 0, table))
	found := map[string]bool{}
	for y, row := range grid {
		line := ""
		for _, c := range row {
			line += string(c.r)
		}
		if x := strings.Index(line, "hchb-readback"); x >= 0 {
			found[Hit(s, 174, 72, table, len([]rune(line[:x])), y)] = true
		}
	}
	if !found["allergies"] {
		t.Fatalf("clicking a climber's name hit %v, want its peak", found)
	}
	_, _, box := place(table, 174, 72)
	if got := Hit(s, 174, 72, table, box.x0+2, box.y0+1); got != "" {
		t.Fatalf("a click on the board hit %q", got)
	}
	if got := Hit(s, 174, 72, table, 1, 0); got != "" {
		t.Fatalf("a click in the sky hit %q", got)
	}
}

// skyLight is the mean brightness of the top rows of a frame, and how many cells there hold a lit star.
func skyLight(t *testing.T, s Scene) (float64, int) {
	t.Helper()
	grid := cells(t, Frame(s, 160, 48, 0, board(10, 0)))
	sum, n, stars := 0.0, 0, 0
	for _, row := range grid[:8] {
		for _, c := range row {
			lum := func(px color.RGBA) float64 {
				return (rgb{float64(px.R) / 255, float64(px.G) / 255, float64(px.B) / 255}).luma()
			}
			sum += lum(c.fg) + lum(c.bg)
			n += 2
			if c.r == '▀' && lum(c.fg) > .6 && lum(c.bg) < .2 || c.r == '▀' && lum(c.bg) > .6 && lum(c.fg) < .2 {
				stars++
			}
		}
	}
	return sum / float64(n), stars
}

func TestTheSkyFollowsTheLocalClockNotTheTerminalTheme(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 1, 15, h, m, 0, 0, time.Local) }
	for _, dark := range []bool{true, false} {
		s := caucasus(dark)
		s.Now = at(12, 0)
		noon, noonStars := skyLight(t, s)
		s.Now = at(0, 0)
		midnight, midnightStars := skyLight(t, s)
		if noon < midnight+.25 {
			t.Errorf("dark=%v: noon sky %.2f is not clearly brighter than midnight %.2f", dark, noon, midnight)
		}
		if noonStars != 0 || midnightStars == 0 {
			t.Errorf("dark=%v: stars at noon %d, at midnight %d", dark, noonStars, midnightStars)
		}
		s.Now = at(11, 59)
		before, _ := skyLight(t, s)
		if d := before - noon; d > .01 || d < -.01 {
			t.Errorf("dark=%v: the sky jumped %.3f in the minute before noon", dark, d)
		}
	}
	day, night := caucasus(true), caucasus(false)
	day.Now, night.Now = at(12, 0), at(12, 0)
	a, _ := skyLight(t, day)
	b, _ := skyLight(t, night)
	if d := a - b; d > .1 || d < -.1 {
		t.Errorf("the terminal theme changed the time of day: dark noon %.2f, light noon %.2f", a, b)
	}
}
