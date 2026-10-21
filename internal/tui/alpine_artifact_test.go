package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func artifactBoard(status *core.PRStatus) core.Board {
	b := alpineBoard()
	b.Projects[0].Sessions[0].PRs = []core.PR{{URL: "https://example.test/ridge/42", Repo: "org/ridge", Number: 42, Status: status}}
	b.Repos[0].Missions[0] = b.Projects[0]
	b.Sessions[0] = b.Projects[0].Sessions[0]
	return b
}

func canvasText(c alpineCanvas) string {
	var rows []string
	for y := range c.h {
		rows = append(rows, ansi.Strip(c.row(y, 0, c.w)))
	}
	return strings.Join(rows, "\n")
}

func TestArtifactScenesShowObservedFacts(t *testing.T) {
	usePalette(t, false)
	for _, tc := range []struct {
		name       string
		status     *core.PRStatus
		want, fact string
		moving     bool
	}{
		{"unfetched", nil, "status unknown", "CI and review unknown", false},
		{"failed fetch", &core.PRStatus{State: "UNKNOWN"}, "status unknown", "CI and review unknown", false},
		{"missing repository", &core.PRStatus{State: core.PRGone}, "status unknown", "CI and review unknown", false},
		{"pending", &core.PRStatus{State: "OPEN", Commits: 3, Passed: 2, Pending: 1}, "CI running", "CI 2/3", true},
		{"failed", &core.PRStatus{State: "OPEN", Commits: 4, Passed: 2, Failed: 1}, "CI failed", "CI 2/3", false},
		{"review", &core.PRStatus{State: "OPEN", Commits: 4, Passed: 3, Threads: 2, Unresolved: 1}, "1 thread open", "1 thread open", false},
		{"changes requested", &core.PRStatus{State: "OPEN", Commits: 4, Passed: 3, Review: "CHANGES_REQUESTED"}, "changes requested", "CI 3/3", false},
		{"approved", &core.PRStatus{State: "OPEN", Commits: 5, Passed: 3, Review: "APPROVED"}, "approved", "5 commits", false},
		{"no CI checks", &core.PRStatus{State: "OPEN", Commits: 2}, "open", "no CI checks", true},
		{"merged", &core.PRStatus{State: "MERGED", Commits: 8}, "merged", "8 commits", false},
		{"closed", &core.PRStatus{State: "CLOSED", Commits: 2}, "closed unmerged", "2 commits", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := artifactBoard(tc.status)
			m := Model{}
			m.alpine.observe(b)
			a := m.alpine.groups["m:ridge"].artifacts[0]
			first := newAlpineCanvas(44, 5)
			m.drawArtifact(&first, a, "m:ridge")
			text := canvasText(first)
			for _, want := range []string{"#42", tc.want, tc.fact} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in %s", want, text)
				}
			}
			m.alpine.frame = 6
			next := newAlpineCanvas(44, 5)
			m.drawArtifact(&next, a, "m:ridge")
			if (string(first.text) != string(next.text)) != tc.moving {
				t.Fatalf("unexpected motion for %s", tc.name)
			}
			if len(m.alpine.events) != 0 {
				t.Fatal("startup replayed an artifact event")
			}
		})
	}
}

func TestArtifactTransitionsAreFreshAndExpire(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after core.PRStatus
		event         string
	}{
		{"merge", core.PRStatus{State: "OPEN", Pending: 1}, core.PRStatus{State: "MERGED"}, "merge"},
		{"failure", core.PRStatus{State: "OPEN", Pending: 1}, core.PRStatus{State: "OPEN", Failed: 1}, "fracture"},
		{"commits", core.PRStatus{State: "OPEN", Commits: 1}, core.PRStatus{State: "OPEN", Commits: 2}, "work"},
		{"closure", core.PRStatus{State: "OPEN"}, core.PRStatus{State: "CLOSED"}, ""},
		{"fetch resolves", core.PRStatus{State: "UNKNOWN"}, core.PRStatus{State: "MERGED", Commits: 8}, ""},
		{"first commit fetch", core.PRStatus{State: "UNKNOWN"}, core.PRStatus{State: "OPEN", Commits: 8}, ""},
		{"fetch fails", core.PRStatus{State: "OPEN", Passed: 3}, core.PRStatus{State: "UNKNOWN"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := tc.before
			b := artifactBoard(&status)
			var a alpine
			a.observe(b)
			status = tc.after
			a.observe(b)
			if tc.event == "" {
				if len(a.events) > 0 {
					t.Fatalf("invented an event: %+v", a.events)
				}
				return
			}
			e, ok := a.kindAt("m:ridge", tc.event)
			if !ok || e.artifact != "https://example.test/ridge/42" {
				t.Fatalf("missing artifact event: %+v", a.events)
			}
			a.observe(b)
			if len(a.events) != 1 {
				t.Fatal("unchanged refresh replayed the event")
			}
			a.frame = 80
			if _, ok := a.kindAt("m:ridge", tc.event); ok {
				t.Fatal("event outlived its transition")
			}
		})
	}
}

func TestArtifactOwnershipDeduplicatesAndBoundsFleetScenes(t *testing.T) {
	for _, n := range []int{10, 1000, 10000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			b := alpineBoard()
			s := &b.Projects[0].Sessions[0]
			for i := range n {
				s.PRs = append(s.PRs, core.PR{URL: fmt.Sprintf("https://example.test/ridge/%d", i), Number: i, Repo: "org/ridge", Status: &core.PRStatus{State: "OPEN", Pending: 1}})
			}
			s.Crew[0].PRs = append([]core.PR(nil), s.PRs...)
			var a alpine
			a.observe(b)
			for _, key := range []string{"*", "r:/repo/ridge", "m:ridge", "s:ridge", "a:ridge/rope"} {
				g := a.groups[key]
				if g.prs != n || g.artifactTotal() != n || len(g.artifacts) > 3 {
					t.Fatalf("%s has incorrect PR ownership: %+v", key, g)
				}
			}
		})
	}
}

func TestBranchConstructionUsesCommitsWithoutDuplicatingItsPR(t *testing.T) {
	for _, ahead := range []int{0, 3} {
		b := alpineBoard()
		s := &b.Projects[0].Sessions[0]
		s.RepoKey, s.Default, s.Branch, s.Ahead = "/repo/ridge/.git", "main", "crossing", ahead
		var a alpine
		a.observe(b)
		g := a.groups["m:ridge"]
		if g.artifactTotal() != 1 || g.prs != 0 || g.artifacts[0].commits != ahead {
			t.Fatalf("branch lost commit facts: %+v", g)
		}
		s.PRs = []core.PR{{URL: "https://example.test/ridge/42", Number: 42, Repo: "org/ridge", Status: &core.PRStatus{State: "OPEN", Head: "crossing", Commits: 3}}}
		a.observe(b)
		if g = a.groups["m:ridge"]; g.artifactTotal() != 1 || g.prs != 1 {
			t.Fatalf("branch counted beside its own PR: %+v", g)
		}
	}
}

func TestMergeRipplesOnlyAfterObservedMerge(t *testing.T) {
	usePalette(t, false)
	b := artifactBoard(&core.PRStatus{State: "OPEN", Pending: 1})
	m := Model{core: &core.Core{}, board: b, loaded: true, width: 160, height: 60, mode: modeRepo, repo: "/repo/ridge"}
	m.alpine.observe(b)
	page := m.prepareView()
	if page.layout.lakeHeight == 0 {
		t.Fatal("spare space has no lake")
	}
	m.alpine.frame = 16
	before := strings.Split(m.paintView(page), "\n")
	b.Projects[0].Sessions[0].PRs[0].Status.State = "MERGED"
	m.alpine.observe(b)
	m.alpine.frame = 32
	m.board = b
	page = m.prepareView()
	during := strings.Split(m.paintView(page), "\n")
	// Compare the same water phase with and without the observed transition.
	noEvent := m
	noEvent.alpine.effects = map[string][]alpineEvent{}
	baseline := strings.Split(noEvent.paintView(page), "\n")
	changed := false
	for y := page.layout.lakeStart; y < page.layout.lakeStart+page.layout.lakeHeight; y++ {
		changed = changed || during[y] != baseline[y]
	}
	if !changed {
		t.Fatal("observed merge has no lake response")
	}
	if len(before) != len(during) {
		t.Fatal("merge moved the table")
	}
	m.alpine.frame = 100
	expired := strings.Split(m.paintView(page), "\n")
	noEvent.alpine.frame = 100
	baseline = strings.Split(noEvent.paintView(page), "\n")
	for y := page.layout.lakeStart; y < page.layout.lakeStart+page.layout.lakeHeight; y++ {
		if expired[y] != baseline[y] {
			t.Fatal("merge ripple did not settle")
		}
	}
}

func TestScenerySelectionAndOverflowKeepFleetCounts(t *testing.T) {
	for _, n := range []int{2, 20, 1000} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			b := core.Board{At: time.Unix(100, 0)}
			for i := range n {
				id, repo := fmt.Sprint("team", i), fmt.Sprint("/repo/", i)
				s := core.Live{Session: core.Session{ID: id, Title: id, Status: core.Busy}, Project: id}
				p := core.Project{Key: id, Name: id, Repo: repo, Sessions: []core.Live{s}}
				b.Projects = append(b.Projects, p)
				b.Repos = append(b.Repos, core.Repo{Key: repo, Name: repo, Missions: []core.Project{p}})
			}
			m := Model{core: &core.Core{}, board: b, loaded: true, width: 160, height: 50, cursor: n*2 - 1}
			m.alpine.observe(b)
			l := m.prepareView().layout
			if l.focus != "m:"+b.Projects[n-1].Key {
				t.Fatalf("selection followed the wrong project: %s", l.focus)
			}
			total, found := 0, false
			for _, g := range l.panorama {
				total += g.total()
				found = found || g.region == b.Projects[n-1].Repo
			}
			if total != n || !found || len(l.panorama) > 160/28 {
				t.Fatalf("fleet missing from skyline: total=%d, selected=%v", total, found)
			}
			oldHue := m.alpine.hues[b.Projects[n-1].Key]
			b.Projects = append([]core.Project{{Key: "new", Name: "new"}}, b.Projects...)
			m.alpine.observe(b)
			if m.alpine.hues[b.Projects[n].Key] != oldHue {
				t.Fatal("new project changed an existing project color")
			}
		})
	}
}

func TestSharedArtifactTransitionsReachEachOwningTeam(t *testing.T) {
	status := core.PRStatus{State: "OPEN", Pending: 1}
	b := artifactBoard(&status)
	session := &b.Projects[0].Sessions[0]
	session.Status = core.Idle
	session.Crew[0].PRs = session.PRs
	var a alpine
	a.observe(b)
	if !a.groups["m:ridge"].artifacts[0].working {
		t.Fatal("shared PR lost its working owner")
	}
	status.Pending, status.Failed = 0, 1
	a.observe(b)
	for _, place := range []string{"m:ridge", "s:ridge", "a:ridge/rope"} {
		if _, ok := a.kindAt(place, "fracture"); !ok {
			t.Fatalf("PR transition missed owner %s", place)
		}
	}
	if _, ok := a.kindAt("a:ridge/camp", "fracture"); ok {
		t.Fatal("PR failure affected a team that does not own it")
	}
}

func TestSelectedProjectSuppliesSkylineArtifact(t *testing.T) {
	usePalette(t, false)
	for _, cursor := range []int{1, 2} {
		b := artifactBoard(&core.PRStatus{State: "OPEN", Failed: 1})
		other := b.Projects[0]
		other.Key, other.Name = "other", "Other crossing"
		other.Sessions = []core.Live{{Project: "other", Session: core.Session{ID: "other", Status: core.Busy, Facts: core.Facts{PRs: []core.PR{{URL: "https://example.test/ridge/43", Repo: "org/ridge", Number: 43, Status: &core.PRStatus{State: "OPEN", Pending: 1}}}}}}}
		b.Projects = append(b.Projects, other)
		b.Repos[0].Missions = append(b.Repos[0].Missions, other)
		m := Model{core: &core.Core{}, board: b, loaded: true, width: 100, height: 40, cursor: cursor}
		m.alpine.observe(b)
		l := m.prepareView().layout
		c := newAlpineCanvas(m.width, l.sky)
		m.drawPanorama(&c, l)
		if !strings.Contains(canvasText(c), fmt.Sprintf("#%d", 41+cursor)) {
			t.Fatalf("selected PR is absent from narrow skyline: %s", canvasText(c))
		}
		if l.panorama[0].prs != 2 {
			t.Fatal("selection displaced repository totals")
		}
	}
}
