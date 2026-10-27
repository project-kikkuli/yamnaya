package mountains

import (
	"fmt"
	"image/color"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestMain(m *testing.M) {
	scene.Colors, scene.ReducedMotion = scene.TrueColor, false
	os.Exit(agenttest.Run(m))
}

var sizes = []struct{ w, h int }{{80, 24}, {100, 30}, {160, 48}, {260, 70}, {40, 12}, {20, 6}, {1, 1}}

func TestFrameFillsExactlyWidthByHeight(t *testing.T) {
	for _, size := range sizes {
		for _, table := range []string{"", board(5, 0), board(80, 0), strings.Repeat("x", 400)} {
			for _, dark := range []bool{true, false} {
				name := fmt.Sprintf("%dx%d/%d/dark=%v", size.w, size.h, len(table), dark)
				t.Run(name, func(t *testing.T) {
					s := caucasus(dark)
					rows := strings.Split(scene.Frame(s, size.w, size.h, 7, table), "\n")
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
		if got := scene.Frame(caucasus(true), size.w, size.h, 0, "x"); got != "" {
			t.Errorf("%dx%d gave %q", size.w, size.h, got)
		}
	}
}

func TestTableTextSurvivesIntact(t *testing.T) {
	for _, size := range sizes[:5] {
		for _, frame := range []int{0, 13, 999} {
			for _, sc := range []scene.Scene{caucasus(true), caucasus(false), {}} {
				table := board(min(10, size.h-2), 0)
				out := strings.Split(ansi.Strip(scene.Frame(sc, size.w, size.h, frame, table)), "\n")
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
	out := scene.Frame(caucasus(true), 80, 24, 3, table)
	if !strings.Contains(out, scene.Reset+" "+table) {
		t.Fatalf("table styling not passed through after a reset")
	}
}

func TestSameFrameSamePicture(t *testing.T) {
	a := scene.Frame(caucasus(true), 120, 40, 42, board(6, 0))
	b := scene.Frame(caucasus(true), 120, 40, 42, board(6, 0))
	if a != b {
		t.Fatal("same inputs drew different pictures")
	}
	if a == scene.Frame(caucasus(true), 120, 40, 46, board(6, 0)) {
		t.Fatal("the next frame did not move")
	}
}

func TestReducedMotionHoldsStill(t *testing.T) {
	scene.ReducedMotion = true
	defer func() { scene.ReducedMotion = false }()
	if scene.Frame(caucasus(true), 120, 40, 1, "") != scene.Frame(caucasus(true), 120, 40, 500, "") {
		t.Fatal("frames differ under reduced motion")
	}
}

// region is the average color and glyphs in the cells around one peak's summit.
func region(t *testing.T, w scene.Weather, dark bool) (rgb, string) {
	t.Helper()
	s := scene.Scene{Dark: dark, Peaks: []scene.Peak{{ID: "a", Weather: scene.Clear}, {ID: "b", Weather: w, Team: team(2), Progress: .5}, {ID: "c", Weather: scene.Clear}}}
	const width, height = 120, 40
	grid := cells(t, scene.Frame(s, width, height, 5, ""))
	p := newPainter(s, width, height, 5)
	p.terrain()
	g := p.geo[1]
	var sum rgb
	var glyphs strings.Builder
	n := 0.0
	for y := max(0, int(g.y)/2); y < min(height, int(g.y+g.height*.8)/2); y++ {
		for x := max(0, int(g.x-g.half*.6)); x < min(width, int(g.x+g.half*.6)); x++ {
			c := grid[y][x]
			sum = rgb{R: sum.R + float64(c.bg.R), G: sum.G + float64(c.bg.G), B: sum.B + float64(c.bg.B)}
			n++
			if c.r != ' ' && c.r != '▀' {
				glyphs.WriteRune(c.r)
			}
		}
	}
	return sum.Scale(1 / n / 255), glyphs.String()
}

func TestEachWeatherShowsAtItsPeak(t *testing.T) {
	for _, dark := range []bool{true, false} {
		clear, clearGlyphs := region(t, scene.Clear, dark)
		storm, stormGlyphs := region(t, scene.Storm, dark)
		erupt, _ := region(t, scene.Eruption, dark)
		summit, summitGlyphs := region(t, scene.Summit, dark)
		clouds, _ := region(t, scene.Clouds, dark)
		if storm.Luma() >= clear.Luma() {
			t.Errorf("dark=%v: storm %.3f not darker than clear %.3f", dark, storm.Luma(), clear.Luma())
		}
		if !strings.ContainsRune(stormGlyphs, '╱') {
			t.Errorf("dark=%v: no rain in the storm", dark)
		}
		if erupt.R-erupt.B <= clear.R-clear.B+.05 {
			t.Errorf("dark=%v: eruption not redder: %+v vs %+v", dark, erupt, clear)
		}
		if !strings.ContainsRune(summitGlyphs, '⚑') || strings.ContainsRune(clearGlyphs, '⚑') {
			t.Errorf("dark=%v: summit flag only on the summited peak: %q vs %q", dark, summitGlyphs, clearGlyphs)
		}
		if summit.R <= clear.R {
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
	s := scene.Scene{Dark: true, Selected: "deep", Ranges: []scene.Range{{ID: "outer", Name: "outer", Peaks: []scene.Peak{{ID: "o1"}, {ID: "o2"}}, Ranges: []scene.Range{
		{ID: "mid", Peaks: []scene.Peak{{ID: "m1"}}, Ranges: []scene.Range{{ID: "inner", Peaks: []scene.Peak{{ID: "deep", Name: "Dykh-Tau"}, {ID: "i2"}}}}},
	}}}}
	out := ansi.Strip(scene.Frame(s, 160, 48, 0, ""))
	if !strings.Contains(out, "Dykh-Tau") {
		t.Fatal("selected nested peak not named")
	}
	flat := scene.Scene{Dark: true, Selected: "deep", Peaks: []scene.Peak{{ID: "o1"}, {ID: "o2"}, {ID: "m1"}, {ID: "deep", Name: "Dykh-Tau"}, {ID: "i2"}}}
	if scene.Frame(s, 160, 48, 0, "") == scene.Frame(flat, 160, 48, 0, "") {
		t.Fatal("nesting drew the same as a flat row of peaks")
	}
	l := plan(s, 160)
	if l.depth != 2 || len(l.massifs) != 3 {
		t.Fatalf("depth %d with %d massifs, want 3 nested ranges at depth 2", l.depth, len(l.massifs))
	}
}

func TestCrowdsMergeAndKeepTheSelection(t *testing.T) {
	s := scene.Scene{Dark: true, Selected: "p250"}
	for i := range 500 {
		s.Peaks = append(s.Peaks, scene.Peak{ID: fmt.Sprint("p", i), Name: fmt.Sprint("peak ", i), Team: team(1)})
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

func fiveHundred() scene.Scene {
	s := scene.Scene{Dark: true}
	for r := range 25 {
		rg := scene.Range{ID: fmt.Sprint("r", r)}
		for k := range 20 {
			rg.Peaks = append(rg.Peaks, scene.Peak{ID: fmt.Sprint(r, "-", k), Weather: scene.Weather(k % 5), Team: team(k % 3), Progress: .5})
		}
		s.Ranges = append(s.Ranges, rg)
	}
	return s
}

func BenchmarkFrame(b *testing.B) {
	s := fiveHundred()
	for i := 0; i < b.N; i++ {
		scene.Frame(s, 300, 90, i, board(20, 0))
	}
}

func TestLaterFramesAndNewSelectionsReuseTheBackdrop(t *testing.T) {
	s := fiveHundred()
	scene.Frame(s, 300, 90, 0, board(20, 0))
	builds := backdrops.builds
	for f := 1; f <= 8; f++ {
		scene.Frame(s, 300, 90, f*4, board(20, 0))
	}
	for _, sel := range []string{"3-4", "0-1", "3-4", ""} {
		s.Selected = sel
		scene.Frame(s, 300, 90, 40, board(20, 0))
	}
	s.Ranges[2].Peaks[4].Team = []scene.Climber{{Name: "renamed", Stance: scene.Waiting}}
	s.Ranges[2].Peaks[4].Name = "renamed"
	s.Ranges[2].Peaks[4].Progress = .9
	scene.Frame(s, 300, 90, 44, board(20, 0))
	if got := backdrops.builds - builds; got != 0 {
		t.Fatalf("%d backdrops painted for later frames, selections and changed names", got)
	}
	for k := range s.Ranges[2].Peaks {
		s.Ranges[2].Peaks[k].Weather = scene.Summit
	}
	scene.Frame(s, 300, 90, 44, board(20, 0))
	if backdrops.builds-builds != 1 {
		t.Fatal("new weather kept the old backdrop")
	}
}

func TestASelectionLightsTheBackdropAsIfPaintedSelected(t *testing.T) {
	cases := []struct {
		name string
		s    scene.Scene
	}{
		{"peaks", scene.Scene{Dark: true, Peaks: []scene.Peak{{ID: "a", Team: team(1)}, {ID: "b", Weather: scene.Storm}, {ID: "c", Weather: scene.Eruption}, {ID: "d", Weather: scene.Summit}}}},
		{"ranges", work()},
		{"crowd", fiveHundred()},
	}
	for _, c := range cases {
		ids := []string{}
		for _, p := range c.s.Peaks {
			ids = append(ids, p.ID)
		}
		for _, r := range c.s.Ranges[:min(4, len(c.s.Ranges))] {
			ids = append(ids, r.ID)
			if len(r.Peaks) > 0 {
				ids = append(ids, r.Peaks[len(r.Peaks)-1].ID)
			}
		}
		for _, id := range ids {
			s := c.s
			s.Selected = id
			want := newPainter(s, 174, 72, 0)
			want.backdrop()
			got := still(s, 174, 72).at(0, scene.Rect{})
			for k := range want.px {
				if want.px[k] != got.px[k] {
					t.Fatalf("%s selecting %s: pixel %d is %v, painted selected it is %v", c.name, id, k, got.px[k], want.px[k])
				}
			}
		}
	}
}

func TestATableGrowsUpFromAFixedBottomAndRedrawsOnlyTheRowsItCovers(t *testing.T) {
	s := work()
	heights := []int{4, 12, 30, 9, 4}
	frames := make([][]string, len(heights))
	boxes := make([]scene.Rect, len(heights))
	scene.Frame(s, 174, 72, 0, board(heights[0], 0))
	builds := backdrops.builds
	for k, n := range heights {
		frames[k] = strings.Split(scene.Frame(s, 174, 72, 0, board(n, 0)), "\n")
		boxes[k] = scene.TableBox(board(n, 0), 174, 72)
		if boxes[k].Y1 != boxes[0].Y1 {
			t.Fatalf("a table of %d rows ends at row %d, one of %d at %d", n+1, boxes[k].Y1, heights[0]+1, boxes[0].Y1)
		}
	}
	if got := backdrops.builds - builds; got != 0 {
		t.Fatalf("%d backdrops painted across table heights", got)
	}
	for k := 1; k < len(heights); k++ {
		top := min(boxes[k].Y0, boxes[k-1].Y0)
		for y := range frames[k] {
			if frames[k][y] != frames[k-1][y] && (y < top || y >= boxes[k].Y1) {
				t.Fatalf("going from %d to %d rows changed row %d, outside the rows the table covers", heights[k-1], heights[k], y)
			}
		}
	}
}

func TestWeatherOfARangeIsItsWorstStormOrAllDone(t *testing.T) {
	cases := []struct {
		own   scene.Weather
		parts []scene.Weather
		want  scene.Weather
	}{
		{scene.Clear, []scene.Weather{scene.Clear, scene.Storm, scene.Clouds}, scene.Storm},
		{scene.Clear, []scene.Weather{scene.Storm, scene.Eruption}, scene.Eruption},
		{scene.Clear, []scene.Weather{scene.Summit, scene.Summit}, scene.Summit},
		{scene.Clear, []scene.Weather{scene.Summit, scene.Clear}, scene.Clear},
		{scene.Clear, []scene.Weather{scene.Clouds, scene.Clouds}, scene.Clouds},
		{scene.Storm, []scene.Weather{scene.Summit}, scene.Storm},
		{scene.Summit, nil, scene.Summit},
		{scene.Clear, nil, scene.Clear},
	}
	for _, c := range cases {
		if got := scene.Combine(c.own, c.parts); got != c.want {
			t.Errorf("combine(%v, %v) = %v, want %v", c.own, c.parts, got, c.want)
		}
	}
}

func TestAnyPeakNameKeepsTheFrameShape(t *testing.T) {
	names := []string{"日本語の山の名前です", "🏔️⛰️🗻", "é́x", "\x1b[31mred\x1b[0m", "a\tb\rc", strings.Repeat("宽", 200), ""}
	table := "日本語テーブル\n🎉 party row\n\x1b[31mred\x1b[0m row"
	for _, name := range names {
		for _, size := range sizes {
			s := scene.Scene{Dark: true, Selected: "b", Ranges: []scene.Range{{ID: "r", Name: name, Peaks: []scene.Peak{{ID: "a", Name: name, Team: team(3)}}}}}
			for i := range 40 {
				s.Peaks = append(s.Peaks, scene.Peak{ID: fmt.Sprint(i), Name: name, Weather: scene.Weather(i % 5), Team: team(i % 4)})
			}
			s.Peaks[1].ID = "b"
			for _, sel := range []string{"b", "r"} {
				s.Selected = sel
				out := scene.Frame(s, size.w, size.h, 3, table)
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
		return scene.Frame(scene.Scene{Dark: true, Peaks: []scene.Peak{{ID: "a", Team: team(4), Progress: progress}}}, 80, 24, 0, "")
	}
	for _, c := range []struct{ odd, end float64 }{{math.NaN(), 0}, {-3, 0}, {math.Inf(-1), 0}, {7, 1}, {math.Inf(1), 1}} {
		if at(c.odd) != at(c.end) {
			t.Errorf("progress %v drew differently from %v", c.odd, c.end)
		}
	}
}

func work() scene.Scene {
	return scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo:august", Name: "august", Peaks: []scene.Peak{
		{ID: "allergies", Name: "Arbiter allergies rollout", Progress: .6, NeedsYou: true,
			Team:  []scene.Climber{{Name: "hchb-readback", Stance: scene.Climbing}, {Name: "fix-18581", Stance: scene.Waiting}, {Name: "free-disk", Stance: scene.Resting}, {Name: "admit", Stance: scene.Resting}},
			Camps: []scene.Camp{{Label: "#18633", State: scene.Failing, Stage: 3}, {Label: "#18757", State: scene.Green, Stage: 4}, {Label: "#18547", State: scene.Merged, Stage: 6}, {Label: "#18581", State: scene.Merged, Stage: 6}}},
		{ID: "workstations", Name: "Workstations concept", Weather: scene.Clouds},
	}}}}
}

func TestWorkReadsFromThePicture(t *testing.T) {
	for _, dark := range []bool{true, false} {
		s := work()
		s.Dark = dark
		out := ansi.Strip(scene.Frame(s, 174, 72, 0, board(20, 0)))
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
	quiet := scene.Scene{Peaks: []scene.Peak{{ID: "a", Weather: scene.Clouds, Team: []scene.Climber{{Name: "r", Stance: scene.Resting}}}}}
	cases := []struct {
		name string
		s    scene.Scene
		want time.Duration
	}{
		{"quiet", quiet, scene.Ambient},
		{"climbing", scene.Scene{Peaks: []scene.Peak{{ID: "a", Team: team(1)}}}, time.Second},
		{"needs you", scene.Scene{Peaks: []scene.Peak{{ID: "a", NeedsYou: true}}}, time.Second},
		{"storm", scene.Scene{Ranges: []scene.Range{{ID: "r", Peaks: []scene.Peak{{ID: "a", Weather: scene.Storm}}}}}, time.Second},
		{"eruption", scene.Scene{Peaks: []scene.Peak{{ID: "a", Weather: scene.Eruption}}}, scene.Step},
	}
	for _, c := range cases {
		if got := scene.Pace(c.s); got != c.want {
			t.Errorf("%s: Pace = %v, want %v", c.name, got, c.want)
		}
	}
	still := scene.Frame(quiet, 120, 40, 0, board(5, 0))
	for f := range 10 {
		if scene.Frame(quiet, 120, 40, f, board(5, 0)) != still {
			t.Fatalf("a quiet scene changed at frame %d, inside one ambient beat", f)
		}
	}
	if scene.Frame(quiet, 120, 40, 10, board(5, 0)) == still {
		t.Fatal("the ambient beat did not move a quiet scene")
	}
}

func TestHitFindsThePeakUnderACell(t *testing.T) {
	s, table := work(), board(20, 0)
	grid := cells(t, scene.Frame(s, 174, 72, 0, table))
	found := map[string]bool{}
	for y, row := range grid {
		line := ""
		for _, c := range row {
			line += string(c.r)
		}
		if x := strings.Index(line, "hchb-readback"); x >= 0 {
			found[scene.Hit(s, 174, 72, table, len([]rune(line[:x])), y)] = true
		}
	}
	if !found["allergies"] {
		t.Fatalf("clicking a climber's name hit %v, want its peak", found)
	}
	box := scene.TableBox(table, 174, 72)
	if got := scene.Hit(s, 174, 72, table, box.X0+2, box.Y0+1); got != "" {
		t.Fatalf("a click on the board hit %q", got)
	}
	if got := scene.Hit(s, 174, 72, table, 1, 0); got != "" {
		t.Fatalf("a click in the sky hit %q", got)
	}
}

// skyLight is the mean brightness of the top rows of a frame, and how many cells there hold a lit star.
func skyLight(t *testing.T, s scene.Scene) (float64, int) {
	t.Helper()
	grid := cells(t, scene.Frame(s, 160, 48, 0, board(10, 0)))
	sum, n, stars := 0.0, 0, 0
	for _, row := range grid[:8] {
		for _, c := range row {
			lum := func(px color.RGBA) float64 {
				return (rgb{R: float64(px.R) / 255, G: float64(px.G) / 255, B: float64(px.B) / 255}).Luma()
			}
			sum += lum(c.fg) + lum(c.bg)
			n += 2
			if c.r == '▀' && lum(c.fg) > .4 && lum(c.bg) < .25 || c.r == '▀' && lum(c.bg) > .4 && lum(c.fg) < .25 {
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
