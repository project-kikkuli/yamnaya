package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func alpineBoard() core.Board {
	s := core.Live{Session: core.Session{ID: "ridge", Status: core.Busy, Crew: []core.Member{
		{ID: "rope", Status: core.Busy}, {ID: "camp", Status: core.Idle},
	}}}
	s.Project = "ridge"
	p := core.Project{Key: "ridge", Name: "North ridge", Repo: "/repo/ridge", RepoName: "Ridge", Sessions: []core.Live{s}}
	return core.Board{At: time.Unix(100, 0), Projects: []core.Project{p}, Sessions: []core.Live{s}, Repos: []core.Repo{{Key: p.Repo, Name: p.RepoName, Missions: []core.Project{p}}}}
}

func TestExpeditionTransitions(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*core.Board)
		want string
	}{
		{"idle rests without claiming a summit", func(b *core.Board) { b.Projects[0].Sessions[0].Crew[0].Status = core.Idle }, ""},
		{"completed crew reaches summit", func(b *core.Board) { b.Projects[0].Sessions[0].Crew[0].Status = core.Done }, "summit"},
		{"cut crew falls without a summit", func(b *core.Board) {
			b.Projects[0].Sessions[0].Crew[0].Status, b.Projects[0].Sessions[0].Crew[0].Cut = core.Done, true
		}, "fall"},
		{"observed work signals activity", func(b *core.Board) { b.Projects[0].Sessions[0].Crew[0].Facts.Tools = map[string]int{"Read": 2} }, "work"},
		{"new crew departs", func(b *core.Board) {
			b.Projects[0].Sessions[0].Crew = append(b.Projects[0].Sessions[0].Crew, core.Member{ID: "new", Status: core.Busy})
		}, "depart"},
		{"disappearance does not invent a kill", func(b *core.Board) { b.Projects, b.Sessions = nil, nil }, ""},
		{"confirmed external kill sweeps the expedition", func(b *core.Board) {
			b.Projects, b.Sessions = nil, nil
			b.Kills = map[string]time.Time{"ridge": b.At}
		}, "storm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var a alpine
			b := alpineBoard()
			a.observe(b)
			tc.edit(&b)
			a.observe(b)
			if tc.want == "" {
				if len(a.events) != 0 {
					t.Fatalf("unexpected events: %+v", a.events)
				}
				return
			}
			if len(a.events) != 1 || a.events[0].kind != tc.want {
				t.Fatalf("events %+v; want %s", a.events, tc.want)
			}
			if tc.want == "storm" && a.events[0].people != 3 {
				t.Fatalf("storm lost the crew: %+v", a.events[0])
			}
			a.observe(b)
			if len(a.events) != 1 {
				t.Fatalf("refresh replayed an event: %+v", a.events)
			}
			for range 80 {
				a.advance()
			}
			if _, shown := a.effectAt("m:ridge"); shown {
				t.Fatal("effect remains visible after it ends")
			}
		})
	}
}

func TestLandscapeStartsFromCurrentStateWithoutReplayingHistory(t *testing.T) {
	b := alpineBoard()
	b.Kills = map[string]time.Time{"ridge": b.At.Add(-time.Minute)}
	b.Projects[0].Sessions[0].Crew[0].Status = core.Done
	b.Projects[0].Sessions[0].Facts.Tools = map[string]int{"Bash": 200}
	var a alpine
	a.observe(b)
	a.observe(b)
	if len(a.events) > 0 {
		t.Fatalf("startup replayed old work: %+v", a)
	}
	child := core.Live{Session: core.Session{ID: "rope", Status: core.Busy}}
	b.Projects[0].Sessions = append(b.Projects[0].Sessions, child, child)
	a.observe(b)
	if len(a.teams) != 3 {
		t.Fatalf("duplicate process or crew identity counted twice: %+v", a.teams)
	}
}

func TestActivityMovesWithoutReadingTheBoardOrMovingInput(t *testing.T) {
	usePalette(t, false)
	m := Model{width: 160, height: 40, loaded: true, mode: modeInput, inKind: inFilter, input: []rune("ridge"), cursor: 2}
	m.board = alpineBoard()
	m.alpine.observe(m.board)
	before := m.View()
	for range 20 {
		next, _ := m.Update(alpineFrame{})
		m = next.(Model)
	}
	if m.View() == before {
		t.Fatal("active team stopped moving")
	}
	if m.cursor != 2 || string(m.input) != "ridge" || m.mode != modeInput {
		t.Fatalf("animation changed the controls: %+v", m)
	}
}

func TestAlpineBoardKeepsControlsVisibleAcrossTerminalSizes(t *testing.T) {
	usePalette(t, false)
	for _, size := range [][2]int{{40, 12}, {80, 24}, {80, 28}, {100, 40}, {160, 50}, {240, 80}} {
		for _, effect := range []string{"", "summit", "storm"} {
			t.Run(fmt.Sprint(size, effect), func(t *testing.T) {
				m, _, _ := setup(t)
				next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
				m = next.(Model)
				if effect != "" {
					m.alpine.emit(alpineEvent{kind: effect, mountain: m.alpine.mountains[0].key, frame: 0, people: 8})
					m.alpine.group()
				}
				for _, frame := range []uint64{0, 15, 40, 70} {
					m.alpine.frame = frame
					view := m.View()
					lines := strings.Split(view, "\n")
					if len(lines) != m.height {
						t.Fatalf("%d lines on %d-row terminal", len(lines), m.height)
					}
					for _, line := range lines {
						if ansi.StringWidth(line) > m.width {
							t.Fatalf("line exceeds %d: %s", m.width, line)
						}
					}
					if !strings.Contains(ansi.Strip(view), "NEEDS YOU") {
						t.Fatalf("landscape displaced the task board:\n%s", view)
					}
				}
			})
		}
	}
}

func TestAnimationPreservesSelectionAndScrollAcrossFrames(t *testing.T) {
	m, _, _ := setup(t)
	m = press(t, m, "down")
	r, _ := m.selected()
	key := rowKey(r)
	for range 90 {
		next, _ := m.Update(alpineFrame{})
		m = next.(Model)
	}
	r, _ = m.selected()
	if rowKey(r) != key {
		t.Fatalf("selection moved from %s to %s", key, rowKey(r))
	}
	m.mode, m.scrollY = modeAgent, 5
	next, _ := m.Update(alpineFrame{})
	if next.(Model).scrollY != 5 {
		t.Fatal("animation reset the detail scroll")
	}
}

func TestSceneryToggleFreesSpaceAndLeavesTypedAInTheInput(t *testing.T) {
	m, _, _ := setup(t)
	m.width = 160
	m.page = nil
	firstRepo := func(view string) int {
		for y, line := range strings.Split(ansi.Strip(view), "\n") {
			if strings.Contains(line, "2 missions") {
				return y
			}
		}
		return -1
	}
	before := firstRepo(m.View())
	m = press(t, m, "A")
	if hidden := firstRepo(m.View()); hidden < 0 || hidden >= before {
		t.Fatalf("hiding scenery did not recover vertical space: before=%d, after=%d", before, hidden)
	}
	if m.sceneWidth() != 0 {
		t.Fatal("A did not hide the landscape")
	}
	m = press(t, m, "A", "/", "A")
	if m.sceneWidth() == 0 || string(m.input) != "A" {
		t.Fatal("typing A toggled the landscape instead of entering text")
	}
}

func TestWideMissionNamesKeepTheirCellsWithAndWithoutColor(t *testing.T) {
	for _, color := range []bool{false, true} {
		t.Run(fmt.Sprint(color), func(t *testing.T) {
			usePalette(t, color)
			b := alpineBoard()
			b.Projects[0].Name = "雪山 🏔 expedition"
			b.Repos[0].Missions[0].Name = b.Projects[0].Name
			m := Model{width: 80, height: 32, board: b, loaded: true}
			m.alpine.observe(b)
			v := m.View()
			if !strings.Contains(ansi.Strip(v), "雪山 🏔") {
				t.Fatal("wide mission name is missing")
			}
			if colored(v) != color || strings.Contains(v, "\x00") {
				t.Fatalf("invalid landscape output: %q", v)
			}
			for _, line := range strings.Split(v, "\n") {
				if ansi.StringWidth(line) > m.width {
					t.Fatalf("wide label overflowed: %q", line)
				}
			}
		})
	}
}

func TestTornadoRequiresSuccessfulKillAndFreshObservation(t *testing.T) {
	for _, stubborn := range []bool{false, true} {
		t.Run(fmt.Sprint("stubborn=", stubborn), func(t *testing.T) {
			m, c, _ := setup(t)
			signals(c, stubborn)
			m = press(t, to(t, m, mission("lead")), "K", "y")
			storms := 0
			for _, e := range m.alpine.events {
				if e.kind == "storm" {
					storms++
				}
			}
			want := 1
			if stubborn {
				want = 0
			}
			if storms != want {
				t.Fatalf("%d tornadoes; want %d", storms, want)
			}
		})
	}
}

func TestFailedBoardRefreshPreservesExpeditions(t *testing.T) {
	m, _, _ := setup(t)
	before := len(m.alpine.teams)
	next, _ := m.Update(boardMsg{err: errors.New("unavailable")})
	m = next.(Model)
	if len(m.alpine.teams) != before || len(m.alpine.events) > 0 {
		t.Fatal("failed refresh changed the landscape")
	}
	_, cmd := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if cmd != nil {
		t.Fatal("resize unexpectedly reread the board")
	}
}

func BenchmarkAlpineFrame(b *testing.B) {
	for _, layout := range []string{"crew", "missions", "artifacts"} {
		for _, n := range []int{100, 1000, 10000} {
			b.Run(fmt.Sprint(layout, "/", n), func(b *testing.B) {
				m := Model{width: 160, height: 50, loaded: true}
				board := alpineBoard()
				if layout == "artifacts" {
					for i := range n {
						board.Projects[0].Sessions[0].PRs = append(board.Projects[0].Sessions[0].PRs, core.PR{URL: fmt.Sprintf("https://example.test/ridge/%d", i), Number: i, Repo: "ridge", Status: &core.PRStatus{State: "OPEN", Pending: 1, Commits: 3}})
					}
				} else if layout == "crew" {
					for i := range n {
						board.Projects[0].Sessions[0].Crew = append(board.Projects[0].Sessions[0].Crew, core.Member{ID: fmt.Sprint("climber", i), Status: core.Busy})
					}
				} else {
					board.Projects = nil
					board.Sessions = nil
					for i := range n {
						id := fmt.Sprint("mission", i)
						session := core.Live{Project: id, Session: core.Session{ID: id, Status: core.Busy}}
						board.Projects = append(board.Projects, core.Project{Key: id, Name: id, Repo: "/repo/ridge", Sessions: []core.Live{session}})
						board.Sessions = append(board.Sessions, session)
					}
				}
				board.Repos[0].Missions = board.Projects
				m.board = board
				m.alpine.observe(board)
				page := m.prepareView()
				m.page = &page
				for b.Loop() {
					m.alpine.advance()
					_ = m.View()
				}
			})
		}
	}
}

func TestDenseExpeditionsKeepEveryStateAndBoundTheirMarkers(t *testing.T) {
	for _, n := range []int{10, 1000, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			b := alpineBoard()
			b.Projects[0].Sessions[0].Crew = nil
			want := [6]int{}
			want[core.Busy]++
			for i := range n {
				status := []core.Status{core.Busy, core.Waiting, core.Idle, core.Done, core.Unknown}[i%5]
				b.Projects[0].Sessions[0].Crew = append(b.Projects[0].Sessions[0].Crew, core.Member{ID: fmt.Sprint(i), Status: status})
				want[status]++
			}
			var a alpine
			a.observe(b)
			for _, key := range []string{"r:/repo/ridge", "m:ridge", "s:ridge"} {
				group := a.groups[key]
				if group == nil || group.counts != want || group.total() != n+1 {
					t.Fatalf("%s omitted agents: %+v; want %v", key, group, want)
				}
				if len(group.sample) > 6 {
					t.Fatalf("%d agents require %d markers", n, len(group.sample))
				}
			}
			b.Kills = map[string]time.Time{"ridge": b.At}
			b.Projects = nil
			a.observe(b)
			if a.groups["r:/repo/ridge"].total() != 0 {
				t.Fatal("departed agents remain in the active fleet")
			}
			if e, ok := a.effectAt("r:/repo/ridge"); !ok || e.people != n+1-want[core.Done] {
				t.Fatalf("kill lost members of its expedition: %+v", e)
			}
		})
	}
}

func TestTerrainAndEventsPreserveEveryVisibleTextCell(t *testing.T) {
	usePalette(t, true)
	for _, mode := range []mode{modeList, modeRepo, modeDetail, modeSession, modeAgent} {
		for _, event := range []string{"", "storm", "summit", "work", "depart"} {
			t.Run(fmt.Sprint(mode, event), func(t *testing.T) {
				m, _, _ := setup(t)
				m.width, m.height = 160, 40
				m.mode, m.detail, m.sess, m.agent = mode, "lead", "lead", "a1"
				if len(m.board.Repos) > 0 {
					m.repo = m.board.Repos[0].Key
				}
				if event != "" {
					m.alpine.emit(alpineEvent{kind: event, mountain: "lead", team: "lead", people: 24})
					m.alpine.group()
				}
				page := m.prepareView()
				m.page = &page
				for _, frame := range []uint64{0, 17, 46, 80} {
					m.alpine.frame = frame
					lines := strings.Split(m.View(), "\n")
					if len(lines) != m.height {
						t.Fatalf("view has %d lines, want %d", len(lines), m.height)
					}
					for y, line := range page.lines {
						if line.scenery != 0 {
							continue
						}
						want := ansi.Strip(line.text)
						if got := ansi.Strip(ansi.Cut(lines[y], 0, ansi.StringWidth(want))); got != want {
							t.Fatalf("row %d text changed: %q, want %q", y, got, want)
						}
						edge := m.width - m.sceneWidth()
						got := pad(ansi.Strip(ansi.Cut(lines[y], 0, edge)), edge)
						quiet := pad(ansi.Strip(ansi.Cut(line.text, 0, edge)), edge)
						if got != quiet {
							t.Fatalf("scenery entered the quiet area at row %d: %q, want %q", y, got, quiet)
						}
						if (y < page.layout.panelStart || y >= page.layout.panelStart+page.layout.panelHeight) && lines[y] != line.text {
							t.Fatalf("unrelated row %d gained scenery: %q", y, lines[y])
						}
						if ansi.StringWidth(lines[y]) > m.width {
							t.Fatalf("row %d overflows", y)
						}
					}
				}
			})
		}
	}
}

func TestAnimatedBoardAcceptsAnswersAndRefreshesItsCachedText(t *testing.T) {
	m, c, h := setup(t)
	for range 10 {
		next, _ := m.Update(alpineFrame{})
		m = next.(Model)
	}
	m = press(t, m, "a", "ctrl+u", "yes", "enter")
	item, _ := c.Store.Item("D1")
	if item.State != core.ItemAnswered || len(h.typed) != 1 {
		t.Fatalf("answer did not reach the session: %+v, %v", item, h.typed)
	}
	if !strings.Contains(m.View(), "NEEDS YOU (1)") {
		t.Fatal("animation retained the answered item in its text cache")
	}
}

func TestInterruptedClimberGetsAWreckMarker(t *testing.T) {
	usePalette(t, false)
	for _, cut := range []bool{false, true} {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			b := alpineBoard()
			b.Projects[0].Sessions[0].Crew[0].Status = core.Done
			b.Projects[0].Sessions[0].Crew[0].Cut = cut
			m := Model{width: 160, height: 40, loaded: true, board: b, mode: modeAgent, detail: "ridge", sess: "ridge", agent: "rope"}
			m.alpine.observe(b)
			page := m.prepareView()
			rendered := strings.Split(m.paintView(page), "\n")
			mark := "⚑"
			if cut {
				mark = "×"
			}
			for y := range page.lines {
				if y >= page.layout.panelStart && y < page.layout.panelStart+page.layout.panelHeight && strings.Contains(ansi.Strip(ansi.Cut(rendered[y], m.width-m.sceneWidth(), m.width)), mark) {
					return
				}
			}
			t.Fatalf("interrupted=%v, marker %q missing", cut, mark)
		})
	}
}

func TestDenseRegionsKeepOwnerNeedsVisible(t *testing.T) {
	usePalette(t, false)
	for _, n := range []int{1, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			b := alpineBoard()
			for i := range n {
				b.Projects[0].Sessions[0].Crew = append(b.Projects[0].Sessions[0].Crew, core.Member{ID: fmt.Sprint("worker", i), Status: core.Busy})
			}
			b.Repos[0].Missions[0] = b.Projects[0]
			b.Sessions[0] = b.Projects[0].Sessions[0]
			for i := range 2 {
				b.Needs = append(b.Needs, core.Need{Project: "ridge", Item: &core.Item{ID: fmt.Sprint("D", i+1), Session: "ridge", Question: "Choose a crossing?", Kind: core.KindDecision, State: core.ItemOpen}})
			}
			m := Model{board: b, width: 160, height: 40, loaded: true}
			m.alpine.observe(b)
			view := m.View()
			if !strings.Contains(view, "2 need you") || !strings.Contains(view, "✦") {
				t.Fatalf("owner needs disappeared in a fleet of %d", n)
			}
		})
	}
}

func TestSceneMotionMeansActivityOrOwnerAttention(t *testing.T) {
	usePalette(t, false)
	for _, tc := range []struct {
		name               string
		status             core.Status
		cut, needs, moving bool
	}{
		{name: "working", status: core.Busy, moving: true},
		{name: "waiting camp", status: core.Waiting},
		{name: "idle camp", status: core.Idle},
		{name: "completed", status: core.Done},
		{name: "interrupted", status: core.Done, cut: true},
		{name: "unknown", status: core.Unknown},
		{name: "needs owner", status: core.Idle, needs: true, moving: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := alpineBoard()
			b.Projects[0].Sessions[0].Crew = nil
			b.Projects[0].Sessions[0].Status = tc.status
			if tc.cut {
				b.Projects[0].Sessions[0].Crew = []core.Member{{ID: "cut", Status: core.Done, Cut: true}}
			}
			if tc.needs {
				b.Needs = []core.Need{{Project: "ridge", Item: &core.Item{ID: "D1", Session: "ridge", Question: "Choose a crossing?", Kind: core.KindDecision, State: core.ItemOpen}}}
			}
			b.Repos[0].Missions[0] = b.Projects[0]
			m := Model{board: b, loaded: true, width: 160, height: 30}
			m.alpine.observe(b)
			group := *m.alpine.groups["m:ridge"]
			before := newAlpineCanvas(44, 6)
			m.drawRopeTeam(&before, group)
			changed := false
			for _, frame := range []uint64{6, 12, 18} {
				m.alpine.frame = frame
				after := newAlpineCanvas(44, 6)
				m.drawRopeTeam(&after, group)
				changed = changed || string(before.text) != string(after.text)
			}
			if changed != tc.moving {
				t.Fatalf("motion=%v, want %v", changed, tc.moving)
			}
		})
	}
}

func TestDenseScenesKeepCountsAndWorkingMotion(t *testing.T) {
	usePalette(t, false)
	for _, count := range []int{10, 1000, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			g := expeditionGroup{mission: "ridge", region: "/repo/ridge", name: "North ridge", cut: count, needs: 3}
			for _, state := range []core.Status{core.Busy, core.Waiting, core.Idle, core.Done, core.Unknown} {
				g.counts[state] = count
			}
			g.counts[core.Done] += count
			m := Model{}
			before := newAlpineCanvas(44, 30)
			m.drawPanel(&before, g, "m:ridge")
			m.alpine.frame = 6
			after := newAlpineCanvas(44, 30)
			m.drawPanel(&after, g, "m:ridge")
			if string(before.text) == string(after.text) {
				t.Fatal("dense active team stopped moving")
			}
			var rows []string
			for y := range after.h {
				rows = append(rows, ansi.Strip(after.row(y, 0, after.w)))
			}
			text := strings.Join(strings.Fields(strings.Join(rows, " ")), " ")
			for _, state := range []string{"working", "waiting", "idle", "done", "unknown", "interrupted"} {
				if !strings.Contains(text, fmt.Sprintf("%d %s", count, state)) {
					t.Fatalf("missing %d %s in %q", count, state, text)
				}
			}
			if !strings.Contains(text, "3 need you") {
				t.Fatal("owner signal disappeared")
			}
		})
	}
}

func TestProjectEventsStayWithTheirOwnTeam(t *testing.T) {
	usePalette(t, false)
	for _, kind := range []string{"summit", "storm", "depart", "work", "fall"} {
		t.Run(kind, func(t *testing.T) {
			b := alpineBoard()
			b.Projects[0].Sessions = append(b.Projects[0].Sessions, core.Live{Session: core.Session{ID: "other", Status: core.Idle}})
			m := Model{board: b}
			m.alpine.observe(b)
			own, sibling := *m.alpine.groups["s:ridge"], *m.alpine.groups["s:other"]
			m.alpine.frame = 17
			beforeOwn, beforeSibling := newAlpineCanvas(44, 25), newAlpineCanvas(44, 25)
			m.drawPanel(&beforeOwn, own, "s:ridge")
			m.drawPanel(&beforeSibling, sibling, "s:other")
			m.alpine.emit(alpineEvent{kind: kind, mountain: "ridge", team: "ridge", people: 3})
			m.alpine.group()
			afterOwn, afterSibling := newAlpineCanvas(44, 25), newAlpineCanvas(44, 25)
			m.drawPanel(&afterOwn, own, "s:ridge")
			m.drawPanel(&afterSibling, sibling, "s:other")
			if string(beforeSibling.text) != string(afterSibling.text) {
				t.Fatalf("%s affected a sibling team", kind)
			}
			if string(beforeOwn.text) == string(afterOwn.text) {
				t.Fatalf("%s had no visible event", kind)
			}
		})
	}
}
