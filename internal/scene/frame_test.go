package scene

import (
	"fmt"
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
	if a == Frame(caucasus(true), 120, 40, 43, board(6, 0)) {
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
	s := Scene{Dark: dark, Peaks: []Peak{{ID: "a", Weather: Clear}, {ID: "b", Weather: w, Climbers: 2, Progress: .5}, {ID: "c", Weather: Clear}}}
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
		if !strings.ContainsRune(clearGlyphs, 'λ') {
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
		s.Peaks = append(s.Peaks, Peak{ID: fmt.Sprint("p", i), Name: fmt.Sprint("peak ", i), Climbers: 1})
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
		climbers += m.climbers
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
			rg.Peaks = append(rg.Peaks, Peak{ID: fmt.Sprint(r, "-", k), Weather: Weather(k % 5), Climbers: k % 3, Progress: .5})
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
			s := Scene{Dark: true, Selected: "b", Ranges: []Range{{ID: "r", Name: name, Peaks: []Peak{{ID: "a", Name: name, Climbers: 3}}}}}
			for i := range 40 {
				s.Peaks = append(s.Peaks, Peak{ID: fmt.Sprint(i), Name: name, Weather: Weather(i % 5), Climbers: i % 4})
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
		return Frame(Scene{Dark: true, Peaks: []Peak{{ID: "a", Climbers: 4, Progress: progress}}}, 80, 24, 0, "")
	}
	for _, c := range []struct{ odd, end float64 }{{math.NaN(), 0}, {-3, 0}, {math.Inf(-1), 0}, {7, 1}, {math.Inf(1), 1}} {
		if at(c.odd) != at(c.end) {
			t.Errorf("progress %v drew differently from %v", c.odd, c.end)
		}
	}
}
