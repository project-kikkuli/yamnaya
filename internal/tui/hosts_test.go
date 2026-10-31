package tui

import (
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type remotes struct {
	boards []core.RemoteBoard
	ran    [][]string
}

func (r *remotes) Hosts() []string {
	var out []string
	for _, b := range r.boards {
		out = append(out, b.Host)
	}
	return out
}
func (r *remotes) Boards() []core.RemoteBoard { return r.boards }
func (r *remotes) Run(host string, args ...string) (string, error) {
	r.ran = append(r.ran, append([]string{host}, args...))
	return "claude app-wt-ok (id pending) in /home/me/app-wt-ok on branch ok\nopen: ym open app-wt-ok", nil
}
func (r *remotes) Attach(host string, args ...string) *exec.Cmd { return exec.Command("true") }

const box = "dev-team-boxmachine.example.net"

// withBox puts one waiting session and one open item from box on the board beside the fixture's own.
func withBox(t *testing.T, m Model, c *core.Core) (Model, *remotes) {
	t.Helper()
	r := &remotes{boards: []core.RemoteBoard{{Host: box, At: time.Now(), Sessions: []core.Live{{
		Session: core.Session{Harness: "claude", ID: "far-lead-0001", PID: 40, Title: "Far work", Status: core.Waiting},
		Project: "far-lead-0001", Remote: box, Where: "ym %3",
	}}, Items: []core.Item{{Seq: 1, ID: "D1", Kind: core.KindDecision, Question: "Merge the far branch?", Session: "far-lead-0001", State: core.ItemOpen, Host: box}},
		Waiting: []core.RemoteWaiting{{Session: "far-lead-0001", Line: "which branch?", Reply: "which branch?"}}}}}
	c.Remotes, c.ShowRemotes = r, true
	m.width, m.peaks.hidden = 140, true
	return refresh(t, m), r
}

func TestRowsFromAnotherHostCarryItsShortNameAndLocalRowsNone(t *testing.T) {
	m, c, _ := setup(t)
	m, _ = withBox(t, m, c)
	lines := strings.Split(ansi.Strip(m.View().Content), "\n")
	line := func(has string) string {
		for _, l := range lines {
			if strings.Contains(l, has) {
				return l
			}
		}
		t.Fatalf("no line with %q in:\n%s", has, strings.Join(lines, "\n"))
		return ""
	}
	for _, c := range []struct {
		row, tag string
	}{
		{"Merge the far branch?", "@boxmachine"},
		{"Far work ", "@boxmachine"},
		{"Lead work ", ""},
		{"Ship it?", ""},
		{"hosts ", "@boxmachine ok"},
	} {
		if l := line(c.row); c.tag != "" && !strings.Contains(l, c.tag) || c.tag == "" && strings.Contains(l, "@") {
			t.Errorf("row %q = %q, want host tag %q", c.row, l, c.tag)
		}
	}
}

func TestTheDetailViewNamesWhereARemoteSessionRuns(t *testing.T) {
	m, c, _ := setup(t)
	m, _ = withBox(t, m, c)
	m = press(t, to(t, m, mission("far-lead-0001")), "enter")
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "@boxmachine ym %3") {
		t.Fatalf("detail view lacks the session's host and pane:\n%s", v)
	}
}

func TestLaunchesStartWhereTheTargetSaysAndTheNextOneDefaultsThere(t *testing.T) {
	for _, c := range []struct {
		name   string
		keys   []string
		remote []string
		local  int
		next   string
	}{
		{"session here", []string{"+", "ok", "enter"}, nil, 1, ""},
		{"session on the host", []string{"+", "ctrl+t", "ok", "enter"}, []string{box, "new", "--repo", "app", "--", "ok"}, 0, box},
		{"codex on the host", []string{"+", "tab", "ctrl+t", "ok", "enter"}, []string{box, "new", "--codex", "--repo", "app", "--", "ok"}, 0, box},
		{"switching back", []string{"+", "ctrl+t", "ctrl+t", "ok", "enter"}, nil, 1, ""},
		{"goal on the host", []string{"N", "ctrl+t", "ship", "enter"}, []string{box, "goal", "--repo", "app", "--", "ship"}, 0, box},
		{"chief on the host", []string{"C", "enter", "ctrl+t", "lead", "enter", "enter"}, []string{box, "chief", "--", "lead"}, 0, box},
		{"chief with a remit on the host", []string{"C", "enter", "ctrl+t", "lead", "enter", "ship", "enter"}, []string{box, "chief", "--remit", "ship", "--", "lead"}, 0, box},
		{"codex goal on the host", []string{"N", "tab", "ctrl+t", "ship", "enter"}, []string{box, "goal", "--codex", "--repo", "app", "--", "ship"}, 0, box},
		{"codex chief on the host", []string{"C", "enter", "tab", "ctrl+t", "lead", "enter", "enter"}, []string{box, "chief", "--codex", "--", "lead"}, 0, box},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			l := &launches{}
			k.Launches = l
			m = place(t, m, k)
			m, r := withBox(t, m, k)
			m = to(t, m, mission("lead"))
			for _, key := range c.keys {
				if len(key) > 1 && key != "enter" && key != "tab" && key != "ctrl+t" {
					m = press(t, m, strings.Split(key, "")...)
					continue
				}
				m = press(t, m, key)
			}
			if c.remote == nil && len(r.ran) != 0 || c.remote != nil && (len(r.ran) != 1 || !reflect.DeepEqual(r.ran[0], c.remote)) {
				t.Fatalf("ran on hosts %v, want %v", r.ran, c.remote)
			}
			if len(l.started) != c.local {
				t.Fatalf("started %d here, want %d", len(l.started), c.local)
			}
			if c.remote != nil && !strings.HasPrefix(m.message, "started on @boxmachine: ") {
				t.Fatalf("message %q", m.message)
			}
			m = press(t, m, "+")
			if m.launchOn != c.next || !strings.Contains(ansi.Strip(strings.Join(m.footer(), "\n")), "local") {
				t.Fatalf("next launch on %q, want %q; footer:\n%s", m.launchOn, c.next, ansi.Strip(strings.Join(m.footer(), "\n")))
			}
		})
	}
}

func TestTheItemsListShowsAnotherHostsClosedItemWithTheHistoryThatHostKept(t *testing.T) {
	m, c, _ := setup(t)
	m, r := withBox(t, m, c)
	r.boards[0].Closed = []core.Item{{Seq: 2, ID: "D2", Kind: core.KindDecision, Question: "Which word?", Session: "far-lead-0001", State: core.ItemAnswered,
		Answer: "D", Host: box, History: []core.Event{{To: core.ItemOpen}, {From: core.ItemOpen, To: core.ItemAnswered, Text: "D"},
			{From: core.ItemAnswered, To: core.ItemAnswered, Text: "ym item answer #2 C did not land: answered there already: D"}}}}
	m = refresh(t, m)
	m = press(t, m, "I")
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "Which word?") {
		t.Fatalf("items list lacks box's answered item:\n%s", v)
	}
	for range len(m.items) {
		if r, ok := m.selected(); ok && r.item != nil && r.item.Host == box && r.item.Seq == 2 {
			break
		}
		m = press(t, m, "down")
	}
	m = press(t, m, "enter")
	if v := ansi.Strip(m.View().Content); m.mode != modeWhy || !strings.Contains(v, "did not land: answered there already: D") {
		t.Fatalf("enter on box's item shows mode %v:\n%s", m.mode, v)
	}
}

func TestFilterMatchesTheHostRowsShowForLocalAndRemoteRows(t *testing.T) {
	m, c, _ := setup(t)
	m, _ = withBox(t, m, c)
	local := m.host
	for _, c := range []struct {
		name, filter string
		show, hide   []string
	}{
		{"short tag", "@boxmachine", []string{"Merge the far branch?"}, []string{"Ship it?", "Rename the flag?"}},
		{"full host", "dev-team-boxmachine.example.net", []string{"Merge the far branch?"}, []string{"Ship it?", "Rename the flag?"}},
		{"local word", "local", []string{"Ship it?", "Rename the flag?"}, []string{"Merge the far branch?"}},
		{"this machine", strings.ToUpper(local), []string{"Ship it?", "Rename the flag?"}, []string{"Merge the far branch?"}},
		{"no host text", "Ship", []string{"Ship it?"}, []string{"Rename the flag?", "Merge the far branch?"}},
		{"unknown host", "@nowhere", nil, []string{"Ship it?", "Merge the far branch?"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := press(t, m, "/", "paste:"+c.filter, "enter")
			out := ansi.Strip(f.View().Content)
			for _, s := range c.show {
				if !strings.Contains(out, s) {
					t.Errorf("filter %q hides %q:\n%s", c.filter, s, out)
				}
			}
			for _, s := range c.hide {
				if strings.Contains(out, s) {
					t.Errorf("filter %q shows %q:\n%s", c.filter, s, out)
				}
			}
		})
	}
}

func TestFilterMatchesTheHostOfGoalsAndChiefs(t *testing.T) {
	m, _, _ := setup(t)
	m.board = core.Board{Tree: []core.Node{
		{Manager: core.Manager{ID: "m1"}, Goal: &core.Goal{ID: "g1", Ask: "near goal"}},
		{Manager: core.Manager{ID: box + "/m2", Host: box}, Goal: &core.Goal{ID: "g2", Ask: "far goal"}},
	}, Remotes: []core.RemoteBoard{{Host: box}}}
	for _, flat := range []bool{false, true} {
		for _, c := range []struct {
			filter string
			want   []string
		}{
			{"@boxmachine", []string{"far goal"}},
			{box, []string{"far goal"}},
			{"local", []string{"near goal"}},
			{"goal", []string{"near goal", "far goal"}},
		} {
			f := m
			f.flat, f.filter = flat, c.filter
			var got []string
			for _, r := range f.rows() {
				if r.node != nil {
					got = append(got, r.node.Goal.Ask)
				}
			}
			slices.Sort(got)
			slices.Sort(c.want)
			if !slices.Equal(got, c.want) {
				t.Errorf("flat=%v filter %q shows %v, want %v", flat, c.filter, got, c.want)
			}
		}
	}
}

func TestFilterMatchesTheHostOfMissionsAndKeepsTheirRepoHeader(t *testing.T) {
	m, _, _ := setup(t)
	far := core.Live{Session: core.Session{ID: "far", Title: "x"}, Remote: box}
	near := core.Live{Session: core.Session{ID: "near", Title: "y"}}
	m.board = core.Board{
		Remotes: []core.RemoteBoard{{Host: box}},
		Repos: []core.Repo{
			{Key: "mixed", Name: "mixedrepo", Missions: []core.Project{
				{Key: "mixed-near", Name: "nearwork", Sessions: []core.Live{near}},
				{Key: "mixed-far", Name: "farwork", Sessions: []core.Live{far}},
			}},
			{Key: "onlylocal", Name: "localrepo", Missions: []core.Project{{Key: "ol", Name: "olwork", Sessions: []core.Live{near}}}},
		},
	}
	for _, c := range []struct {
		filter   string
		missions []string
		repos    []string
	}{
		{"@boxmachine", []string{"farwork"}, []string{"mixedrepo"}},
		{box, []string{"farwork"}, []string{"mixedrepo"}},
		{"local", []string{"nearwork", "olwork"}, []string{"mixedrepo", "localrepo"}},
		{"mixedrepo", []string{"nearwork", "farwork"}, []string{"mixedrepo"}},
		{"@nowhere", nil, nil},
	} {
		t.Run(c.filter, func(t *testing.T) {
			f := m
			f.filter = c.filter
			var missions, repos []string
			for _, r := range f.rows() {
				switch {
				case r.project != nil:
					missions = append(missions, r.project.Name)
				case r.repo != nil:
					repos = append(repos, r.repo.Name)
				}
			}
			slices.Sort(missions)
			slices.Sort(c.missions)
			slices.Sort(repos)
			slices.Sort(c.repos)
			if !slices.Equal(missions, c.missions) || !slices.Equal(repos, c.repos) {
				t.Fatalf("filter %q shows missions %v repos %v, want %v %v", c.filter, missions, repos, c.missions, c.repos)
			}
		})
	}
}

func TestHeaderWrapsBetweenItemsToTheWidthAndNamesHostsByNickname(t *testing.T) {
	now := time.Now()
	limit := func(harness, name string, used float64, seen time.Time) core.Limit {
		return core.Limit{Harness: harness, Name: name, Used: used, Window: 7 * 24 * time.Hour, ResetsAt: now.Add(6*time.Hour + 12*time.Minute), Seen: seen}
	}
	states := []struct {
		name  string
		board core.RemoteBoard
		brief string
	}{
		{"answering", core.RemoteBoard{At: now}, "@boxmachine ok"},
		{"slow", core.RemoteBoard{At: now.Add(-12 * time.Second), Slow: true}, "@boxmachine slow 12s"},
		{"not answering", core.RemoteBoard{At: now.Add(-7 * time.Minute), FailingSince: now.Add(-5 * time.Minute),
			Err: errors.New("exit status 255: Connection timed out during banner exchange")}, "@boxmachine no answer 5m"},
	}
	others := []string{"ci-alpha-runner.example.net", "ci-bravo-runner.example.net", "ci-charlie-runner.example.net", "ci-delta-runner.example.net"}
	for _, width := range []int{80, 120, 200} {
		for _, st := range states {
			t.Run(fmt.Sprintf("%d %s", width, st.name), func(t *testing.T) {
				m, c, _ := setup(t)
				c.Meter = meter{core.Fleet{Ready: true, At: now, Limits: []core.Limit{
					limit("claude", "5h", 3, now), limit("claude", "7d", 55, now), limit("codex", "codex 7d", 0, now)}}}
				b := st.board
				b.Host, b.Usage = box, &core.Fleet{Ready: true, At: now, Limits: []core.Limit{limit("claude", "7d", 46, now.Add(-4*time.Hour-2*time.Minute))}}
				boards := []core.RemoteBoard{b}
				for _, h := range others {
					boards = append(boards, core.RemoteBoard{Host: h, At: now})
				}
				c.Remotes, c.ShowRemotes = &remotes{boards: boards}, true
				m.width, m.peaks.hidden = width, true
				m = refresh(t, m)
				m.board.At = now
				header := ansi.Strip(m.header())
				t.Logf("header at %d columns:\n%s", width, header)
				lines := strings.Split(header, "\n")
				for _, l := range lines {
					if ansi.StringWidth(l) >= width {
						t.Errorf("line is %d columns wide on a %d-column board: %q", ansi.StringWidth(l), width, l)
					}
				}
				items := []string{"hosts " + st.brief, "@alpha-runner ok", "@delta-runner ok", "claude 5h 3% ↻6h12m · 7d 55% ↻6h12m",
					"@boxmachine/claude 7d 46% ↻6h12m (4h2m old)", "codex 7d 0% ↻6h12m", "spend today $0.00 · 7d $0.00"}
				for _, want := range items {
					if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }) {
						t.Errorf("no line holds %q whole", want)
					}
				}
				for i, l := range lines[1:] {
					if !strings.HasPrefix(l, " hosts ") && !strings.HasPrefix(l, " usage ") && !strings.HasPrefix(l, "       ") {
						t.Errorf("line %d is neither a header line nor a continuation indented under its label: %q", i+1, l)
					}
				}
				if wraps := len(lines) > 3; wraps != (width < 200) {
					t.Errorf("at %d columns the header takes %d lines", width, len(lines))
				}
				if strings.Contains(header, box) || strings.Contains(header, "exit status") {
					t.Errorf("header shows the full host name or its error; H does")
				}
			})
		}
	}
}

func TestBoardNamesARenamedHostByItsNicknameAndTheFilterTakesBothNames(t *testing.T) {
	m, c, _ := setup(t)
	if err := c.Store.RecordHost(box, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.RenameHost(box, "far"); err != nil {
		t.Fatal(err)
	}
	m, _ = withBox(t, m, c)
	v := ansi.Strip(m.View().Content)
	if !strings.Contains(v, "hosts @far ok") || !strings.Contains(v, "@far Far work") || strings.Contains(v, "@boxmachine") {
		t.Fatalf("board after renaming %s to far:\n%s", box, v)
	}
	for _, f := range []string{"@far", "boxmachine.example"} {
		v := ansi.Strip(press(t, m, "/", "paste:"+f, "enter").View().Content)
		if !strings.Contains(v, "Merge the far branch?") || strings.Contains(v, "Ship it?") {
			t.Errorf("filter %q should keep exactly the renamed host's rows:\n%s", f, v)
		}
	}
	m = press(t, m, "H")
	if v := ansi.Strip(m.View().Content); !strings.Contains(v, "@far") || !strings.Contains(v, box) {
		t.Errorf("H should show the nickname and the full name:\n%s", v)
	}
}

func TestLaunchesInAnotherHostsRepositoryStartOnThatHost(t *testing.T) {
	for _, c := range []struct {
		name string
		keys []string
		want []string
	}{
		{"goal", []string{"N", "ship", "enter"}, []string{box, "goal", "--repo", "august", "--", "ship"}},
		{"session", []string{"+", "ok", "enter"}, []string{box, "new", "--repo", "august", "--", "ok"}},
		{"chief", []string{"C", "enter", "lead", "enter", "enter"}, []string{box, "chief", "--", "lead"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			l := &launches{}
			k.Launches = l
			m = place(t, m, k)
			m, r := withBox(t, m, k)
			r.boards[0].Sessions[0].RepoKey = "/nonexistent/box/august/.git"
			m = to(t, refresh(t, m), mission("far-lead-0001"))
			for _, key := range c.keys {
				if len(key) > 1 && key != "enter" {
					m = press(t, m, strings.Split(key, "")...)
					continue
				}
				m = press(t, m, key)
			}
			if len(r.ran) != 1 || !reflect.DeepEqual(r.ran[0], c.want) || len(l.started) != 0 {
				t.Fatalf("ran on hosts %v and started %d here, want only %v", r.ran, len(l.started), c.want)
			}
		})
	}
}
