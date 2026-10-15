package tui

import (
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func usePalette(t *testing.T, color bool) {
	t.Helper()
	r := lipgloss.NewRenderer(io.Discard)
	r.SetColorProfile(termenv.ANSI)
	old := pal
	pal = newPalette(r, color)
	t.Cleanup(func() { pal = old })
}

func missionBoard(now time.Time) core.Board {
	live := func(id string, st core.Status, doing core.Action, crew ...core.Member) core.Live {
		return core.Live{Session: core.Session{ID: id, Status: st, Facts: core.Facts{Doing: doing}, Cwd: "/wt/" + id, Crew: crew}}
	}
	done := core.Member{ID: "a", Name: "a", Status: core.Done}
	busy := core.Member{ID: "b", Name: "b", Status: core.Busy}
	pr := func(n int, st *core.PRStatus) core.PR {
		return core.PR{URL: fmt.Sprint("https://x/pull/", n), Number: n, Status: st}
	}
	var many []core.PR
	for n := range 12 {
		many = append(many, pr(100+n, nil))
	}
	b := core.Board{At: now, Fleet: core.Fleet{Ready: true}, Projects: []core.Project{
		{Key: "full", Name: "Ship the importer", Status: core.Busy, Spend: core.Spend{USD: 173.4},
			Phases:   core.Phases{Names: []string{"design", "implement", "validate", "ship"}, Current: 2},
			Vibes:    []core.Vibe{{Place: "North Dakota", At: now}},
			Sessions: []core.Live{live("full", core.Busy, core.Action{Tool: "Bash", Detail: "go test ./...", At: now.Add(-5 * time.Minute)}, done, busy)}},
		{Key: "bare", Name: "x", Status: core.Idle,
			Sessions: []core.Live{live("bare", core.Idle, core.Action{})}},
		{Key: "long", Name: strings.Repeat("A very long mission name ", 4), Status: core.Waiting, Spend: core.Spend{USD: 0.5},
			Phases:   core.Phases{Names: strings.Fields("a b c d e f g h i j"), Current: 9},
			Sessions: []core.Live{live("long", core.Waiting, core.Action{Tool: "Read", Detail: strings.Repeat("x", 300), At: now})}},
		{Key: "gone", Name: "Gone", Dormant: []core.Dormant{{Key: "gone"}}},
	}}
	b.Projects[0].Sessions[0].PRs = []core.PR{pr(1, &core.PRStatus{State: "OPEN", Failed: 1}), pr(2, &core.PRStatus{State: "OPEN", Passed: 1}), pr(3, &core.PRStatus{State: "MERGED"})}
	b.Projects[2].Sessions[0].PRs = many
	return b
}

// x is the cell where want first starts in a rendered row, or -1.
func x(row, want string) int {
	row = ansi.Strip(row)
	i := strings.Index(row, want)
	if i < 0 {
		return -1
	}
	return ansi.StringWidth(row[:i])
}

func TestMissionColumnsStartAtOneXWhateverIsMissing(t *testing.T) {
	usePalette(t, true)
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	for _, width := range []int{100, 160, 220} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			m := Model{board: missionBoard(now), loaded: true, width: width, cursor: -1}
			c := m.missionCols(2)
			rows := map[string]string{}
			for _, p := range m.board.Projects {
				rows[p.Key] = m.missionLine(0, p, c)
				if w := ansi.StringWidth(strings.TrimRight(rows[p.Key], "\n")); w > width {
					t.Fatalf("%s row is %d cells on a %d screen: %q", p.Key, w, width, ansi.Strip(rows[p.Key]))
				}
			}
			columns := []struct {
				name string
				cell map[string]string
			}{
				{"phase", map[string]string{"full": "validate", "long": "j "}},
				{"state", map[string]string{"full": "busy", "bare": "idle", "long": "waiting", "gone": "exited"}},
				{"vibe", map[string]string{"full": "North Dakota", "bare": "no vibe", "long": "no vibe", "gone": "no vibe"}},
				{"spend", map[string]string{"full": "$173", "bare": "$0.00", "long": "$0.50", "gone": "$0.00"}},
				{"running crew", map[string]string{"full": "1▶"}},
				{"pull requests", map[string]string{"full": "2PR", "long": "12PR"}},
				{"failing", map[string]string{"full": "✗1"}},
			}
			if c.doing > 0 {
				columns = append(columns, struct {
					name string
					cell map[string]string
				}{"doing", map[string]string{"full": "Bash:", "long": "Read:"}})
			}
			for _, col := range columns {
				at := map[int][]string{}
				for key, cell := range col.cell {
					at[x(rows[key], cell)] = append(at[x(rows[key], cell)], key)
				}
				if len(at) != 1 || at[-1] != nil {
					t.Errorf("%s column starts at %v\n%s", col.name, at, ansi.Strip(strings.Join(slices.Collect(maps.Values(rows)), "")))
				}
			}
			if width >= 160 && !strings.Contains(ansi.Strip(rows["full"]), "5m ago") {
				t.Errorf("doing lost its age at %d: %q", width, ansi.Strip(rows["full"]))
			}
		})
	}
}

var sgr = regexp.MustCompile(`\x1b\[([0-9;]*)m`)

// colored reports whether any escape in s sets a foreground or background color.
func colored(s string) bool {
	for _, m := range sgr.FindAllStringSubmatch(s, -1) {
		for _, p := range strings.Split(m[1], ";") {
			if n, err := strconv.Atoi(p); err == nil && (n >= 30 && n <= 49 || n >= 90 && n <= 107) {
				return true
			}
		}
	}
	return false
}

func TestNoColorKeepsBoldAndFaintButDrawsNoColor(t *testing.T) {
	for _, color := range []bool{true, false} {
		t.Run(fmt.Sprint("color=", color), func(t *testing.T) {
			usePalette(t, color)
			m, _, _ := setup(t)
			m = refresh(t, m)
			v := m.View()
			if colored(v) != color {
				t.Fatalf("color %v but board colored = %v:\n%q", color, colored(v), v)
			}
			if !strings.Contains(v, "\x1b[1m") || !strings.Contains(v, "\x1b[2m") {
				t.Fatalf("bold or faint missing:\n%q", v)
			}
		})
	}
}

func TestNoColorEnvTurnsOffEveryColor(t *testing.T) {
	for _, env := range []string{"", "1", "true"} {
		t.Run("NO_COLOR="+env, func(t *testing.T) {
			t.Setenv("NO_COLOR", env)
			p := defaultPalette()
			for _, s := range append([]lipgloss.Style{p.red, p.yellow, p.green}, p.hues...) {
				_, none := s.GetForeground().(lipgloss.NoColor)
				if none != (env != "") {
					t.Fatalf("NO_COLOR=%q gives foreground %v", env, s.GetForeground())
				}
			}
		})
	}
}

func TestProjectHueIsStableAndSpreadsOverColorsNoStateUses(t *testing.T) {
	usePalette(t, true)
	for _, c := range hueColors {
		if slices.Contains([]string{"1", "2", "3"}, c) {
			t.Fatalf("hue %s is a state color", c)
		}
	}
	seen := map[int]bool{}
	for i := range 30 {
		key := fmt.Sprintf("/Users/someone/dev/repo-%d", i)
		if pal.hue(key).Render("id") != pal.hue(key).Render("id") || hueIndex(key) != hueIndex(key) {
			t.Fatalf("%s changes hue between calls", key)
		}
		seen[hueIndex(key)] = true
	}
	if len(seen) < len(hueColors)-1 {
		t.Fatalf("30 keys reach only %d of %d hues", len(seen), len(hueColors))
	}
}

func TestShortActionKeepsWhatTellsActionsApart(t *testing.T) {
	t.Setenv("HOME", "/h")
	for _, tc := range []struct {
		a    core.Action
		dirs []string
		want string
	}{
		{core.Action{Tool: "Bash", Detail: "/h/go/bin/ym vibe California"}, nil, "Bash: ym vibe California"},
		{core.Action{Tool: "Bash", Detail: "tail -n 5 /wt/tmp/runs/2026/capacity.log"}, []string{"/wt"}, "Bash: tail -n 5 tmp/…/capacity.log"},
		{core.Action{Tool: "Bash", Detail: "cat /wt/a/b.txt"}, []string{"", "/wt"}, "Bash: cat a/b.txt"},
		{core.Action{Tool: "Read", Detail: "/h/notes/a/b/c.md"}, []string{"/wt"}, "Read: ~/…/c.md"},
		{core.Action{Tool: "SendMessage", Detail: "to aadmit-implementer"}, nil, "SendMessage: to aadmit-implementer"},
		{core.Action{Tool: "SendMessage", Detail: "to a0bcb2f4-1c2d-4e5f-9a8b-112233445566"}, nil, "SendMessage: to a0bcb2…"},
		{core.Action{Tool: "Bash", Detail: "ym --session=7fccee8d-d83c-42d6 item inbox"}, nil, "Bash: ym item inbox"},
		{core.Action{Tool: "Bash", Detail: `ym --session "$CODEX_SESSION_ID" phase next`}, nil, "Bash: ym phase next"},
		{core.Action{Tool: "Bash", Detail: `ym -p "$P" item add`}, nil, "Bash: ym item add"},
		{core.Action{Tool: "Bash", Detail: `git commit --message "fix the flag" -q`}, nil, "Bash: git commit -q"},
		{core.Action{Tool: "Bash", Detail: `git commit -m "fix it" --amend`}, nil, `Bash: git commit -m "fix it" --amend`},
		{core.Action{Tool: "Bash", Detail: "go test -run TestX --count 1 ./..."}, nil, "Bash: go test -run TestX --count 1 ./..."},
		{core.Action{Tool: "SendMessage", Detail: "to adev1-validate-c7689032a95fae3c"}, nil, "SendMessage: to adev1-validate-c76890…"},
		{core.Action{Tool: "Bash", Detail: "echo deadbeefcafebabe"}, nil, "Bash: echo deadbeefcafebabe"},
		{core.Action{Tool: "WebFetch", Detail: "https://example.com/a/b/c/d"}, nil, "WebFetch: https://example.com/a/b/c/d"},
		{core.Action{Tool: "Read"}, nil, "Read"},
	} {
		if got := shortAction(tc.a, tc.dirs...); got != tc.want {
			t.Errorf("shortAction(%q) = %q, want %q", tc.a.String(), got, tc.want)
		}
	}
}

func TestDoingCellClipsTheActionAndKeepsItsAge(t *testing.T) {
	usePalette(t, false)
	now := time.Now()
	s := core.Live{Session: core.Session{Facts: core.Facts{Doing: core.Action{Tool: "Bash", Detail: strings.Repeat("make ", 40), At: now.Add(-3 * time.Hour)}}}}
	for _, w := range []int{12, 20, 60} {
		got := ansi.Strip(doingCell(now, s, w))
		if ansi.StringWidth(got) > w || !strings.HasSuffix(got, "3h ago") {
			t.Errorf("width %d: %q", w, got)
		}
	}
}

func TestProjectsOnOneBoardGetDifferentHuesWhileThereAreSpare(t *testing.T) {
	for _, n := range []int{2, 4, len(hueColors), len(hueColors) + 3} {
		t.Run(strconv.Itoa(n), func(t *testing.T) {
			var keys []string
			for i := range n {
				keys = append(keys, fmt.Sprintf("/Users/someone/dev/repo-%d", i*7))
			}
			slots := hueSlots(keys)
			slices.Reverse(keys)
			if again := hueSlots(keys); !maps.Equal(slots, again) {
				t.Fatalf("hues depend on board order: %v then %v", slots, again)
			}
			used := map[int]int{}
			for _, i := range slots {
				used[i]++
			}
			if len(used) != min(n, len(hueColors)) {
				t.Fatalf("%d projects share hues %v", n, used)
			}
		})
	}
}
