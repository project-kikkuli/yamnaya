package tui

import (
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

func TestShortHostsKeepTheShortestTailNoOtherHostShares(t *testing.T) {
	for _, c := range []struct {
		name  string
		hosts []string
		want  map[string]string
	}{
		{"one host", []string{box}, map[string]string{box: "boxmachine"}},
		{"shared tail grows", []string{"a-prod-box.x", "b-prod-box.y"}, map[string]string{"a-prod-box.x": "a-prod-box", "b-prod-box.y": "b-prod-box"}},
		{"distinct tails", []string{"stg-runner.x", "prod-builder.x"}, map[string]string{"stg-runner.x": "runner", "prod-builder.x": "builder"}},
		{"user and plain name", []string{"me@laptop"}, map[string]string{"me@laptop": "laptop"}},
		{"address", []string{"10.0.0.7"}, map[string]string{"10.0.0.7": "10.0.0.7"}},
		{"same label everywhere", []string{"box.a", "box.b"}, map[string]string{"box.a": "box.a", "box.b": "box.b"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := shortHosts(c.hosts); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("shortHosts(%v) = %v, want %v", c.hosts, got, c.want)
			}
		})
	}
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
		{box, "@boxmachine"},
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
		{"chief on the host", []string{"C", "enter", "ctrl+t", "lead", "enter", "enter"}, []string{box, "chief", "--repo", "app", "--", "lead"}, 0, box},
		{"chief with a remit on the host", []string{"C", "enter", "ctrl+t", "lead", "enter", "ship", "enter"}, []string{box, "chief", "--remit", "ship", "--repo", "app", "--", "lead"}, 0, box},
		{"codex goal on the host", []string{"N", "tab", "ctrl+t", "ship", "enter"}, []string{box, "goal", "--codex", "--repo", "app", "--", "ship"}, 0, box},
		{"codex chief on the host", []string{"C", "enter", "tab", "ctrl+t", "lead", "enter", "enter"}, []string{box, "chief", "--codex", "--repo", "app", "--", "lead"}, 0, box},
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
