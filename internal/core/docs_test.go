package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func docRows(b Board) []string {
	var out []string
	for _, n := range b.Needs {
		if n.Doc != nil {
			out = append(out, n.Doc.Key+"@"+n.Doc.Version)
		}
	}
	return out
}

func (w *orgWorld) route(t *testing.T) Board {
	t.Helper()
	if err := w.core.RouteDocs(w.board(t)); err != nil {
		t.Fatal(err)
	}
	return w.board(t)
}

func (w *orgWorld) told(id, key string) int {
	n := 0
	for _, text := range w.handedTo(id) {
		if strings.Contains(text, key) {
			n++
		}
	}
	return n
}

func TestDocAsksAboutAGoalsWorkReachItsManagerAndTheRestReachTheOwner(t *testing.T) {
	pr := "https://github.com/acme/app/pull/7"
	for _, tc := range []struct {
		name string
		ask  func(w *orgWorld, brief string) DocAsk
		mgr  bool
	}{
		{"unrelated page", func(*orgWorld, string) DocAsk {
			return DocAsk{Page: "11111111111111111111111111111111", Refs: []string{"https://github.com/acme/other/pull/1"}}
		}, false},
		{"page names the goal's pull request", func(*orgWorld, string) DocAsk {
			return DocAsk{Page: "11111111111111111111111111111111", Refs: []string{pr}}
		}, true},
		{"goal's brief links the page", func(w *orgWorld, brief string) DocAsk {
			if err := os.WriteFile(brief, []byte("Spec: https://www.notion.so/acme/Launch-2222222222222222222222222222aaaa\n"), 0o644); err != nil {
				panic(err)
			}
			return DocAsk{Page: "2222222222222222222222222222aaaa"}
		}, true},
		{"goal's ask names the page", func(*orgWorld, string) DocAsk {
			return DocAsk{Page: "33333333333333333333333333333333"}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "finish the launch doc https://www.notion.so/acme/Launch-33333333-3333-3333-3333-333333333333", "/repo/app")
			brief := filepath.Join(t.TempDir(), "brief.md")
			g.BriefPath = brief
			if err := w.core.Store.SaveGoal(g); err != nil {
				t.Fatal(err)
			}
			w.spawn("agent", m.Session)
			w.set("agent", func(s *Session) { s.PRs = []PR{{URL: pr, Repo: "acme/app", Number: 7}} })
			a := tc.ask(w, brief)
			a.Key, a.Kind, a.URL, a.Title, a.Asker, a.Text, a.Evidence, a.Version = "notion:d1", "mention", "https://www.notion.so/x?d=d1", "Launch", "Alice", "Can you sign off?", "Alice: Can you sign off?", "c1"
			if err := w.core.Store.SyncDocPage("notion", a.Page, "e1", []DocAsk{a}); err != nil {
				t.Fatal(err)
			}
			if rows := docRows(w.board(t)); len(rows) != 0 {
				t.Fatalf("an unrouted ask reached the owner before routing: %q", rows)
			}
			var b Board
			for range 3 {
				b = w.route(t)
			}
			rows, told := docRows(b), w.told(m.Session, "Can you sign off?")
			if tc.mgr && (len(rows) != 0 || told != 1) {
				t.Fatalf("goal ask: owner rows %q, manager told %d times; want no owner row and one notice", rows, told)
			}
			if !tc.mgr && (!slices.Equal(rows, []string{"notion:d1@c1"}) || told != 0) {
				t.Fatalf("unrelated ask: owner rows %q, manager told %d; want the owner row and no notice", rows, told)
			}
			a.Version, a.Text = "c2", "Still need your sign-off"
			if err := w.core.Store.SyncDocPage("notion", a.Page, "e1", []DocAsk{a}); err != nil {
				t.Fatal(err)
			}
			b = w.route(t)
			if tc.mgr && w.told(m.Session, "Still need your sign-off") != 1 {
				t.Fatalf("a new comment on a routed ask was not handed to the manager")
			}
			if !tc.mgr && !slices.Equal(docRows(b), []string{"notion:d1@c2"}) {
				t.Fatalf("owner rows after a new comment %q", docRows(b))
			}
			if err := w.core.Store.SyncDocPage("notion", a.Page, "e2", nil); err != nil {
				t.Fatal(err)
			}
			if rows := docRows(w.route(t)); len(rows) != 0 {
				t.Fatalf("an ask resolved at the source still shows: %q", rows)
			}
		})
	}
}

func TestADismissedDocAskReturnsOnlyWhenSomeoneWritesAgain(t *testing.T) {
	w := newOrgWorld(t)
	ask := func(version string) {
		a := DocAsk{Key: "notion:d1", Kind: "comment", URL: "https://www.notion.so/p?d=d1", Page: "p", Asker: "Bo", Text: "thoughts?", Version: version}
		if err := w.core.Store.SyncDocPage("notion", a.Page, "e1", []DocAsk{a}); err != nil {
			t.Fatal(err)
		}
	}
	ask("c1")
	b := w.route(t)
	for _, n := range b.Needs {
		if n.Doc != nil {
			if err := w.core.Store.DismissQueue(n.DismissKeys()...); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, step := range []struct {
		version string
		want    []string
	}{{"c1", nil}, {"c1", nil}, {"c2", []string{"notion:d1@c2"}}} {
		ask(step.version)
		if got := docRows(w.route(t)); !slices.Equal(got, step.want) {
			t.Fatalf("version %s: rows %q, want %q", step.version, got, step.want)
		}
	}
}

func TestADocAskWhoseGoalEndsClimbsToTheOwner(t *testing.T) {
	for _, end := range []string{GoalDone, GoalDropped} {
		t.Run(end, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "draft https://www.notion.so/44444444444444444444444444444444", "/repo/app")
			a := DocAsk{Key: "notion:d9", Kind: "mention", Page: "44444444444444444444444444444444", Asker: "Cy", Text: "ok?", Version: "c1"}
			if err := w.core.Store.SyncDocPage("notion", a.Page, "e1", []DocAsk{a}); err != nil {
				t.Fatal(err)
			}
			if rows := docRows(w.route(t)); len(rows) != 0 || w.told(m.Session, "ok?") != 1 {
				t.Fatalf("ask about a running goal: rows %q", rows)
			}
			g.State = end
			if err := w.core.Store.SaveGoal(g); err != nil {
				t.Fatal(err)
			}
			if rows := docRows(w.route(t)); !slices.Equal(rows, []string{"notion:d9@c1"}) {
				t.Fatalf("ask about an ended goal: rows %q, want it in the owner queue", rows)
			}
		})
	}
}

// pageRows lists each doc row as its page and its threads' askers, newest first.
func pageRows(b Board) []string {
	var out []string
	for _, n := range b.Needs {
		if n.Doc == nil {
			continue
		}
		row := n.Doc.Page + ":"
		for _, d := range n.Docs {
			row += " " + d.Asker
		}
		out = append(out, row)
	}
	slices.Sort(out)
	return out
}

func TestADocsOpenThreadsShareOneQueueRowNewestFirstAndResolveOneByOne(t *testing.T) {
	ask := func(key, page, asker, text, at string) DocAsk {
		return DocAsk{Key: key, Kind: "reply", URL: "https://www.notion.so/" + page + "?d=" + key, Page: page, Title: page, Asker: asker,
			Text: text, Version: key + "@" + at, At: at}
	}
	strategy := []DocAsk{
		ask("d1", "strategy", "Ann", "why now?", "2026-10-20T10:00:00Z"),
		ask("d2", "strategy", "Bo", "lgtm", "2026-10-22T09:00:00Z"),
		ask("d3", "strategy", "Cy", "which metric?", "2026-10-21T10:00:00Z"),
	}
	other := []DocAsk{ask("d4", "notes", "Di", "ok?", "2026-10-21T08:00:00Z")}
	w := newOrgWorld(t)
	sync := func(page string, asks []DocAsk) {
		if err := w.core.Store.SyncDocPage("notion", page, "e1", asks); err != nil {
			t.Fatal(err)
		}
	}
	sync("strategy", strategy)
	sync("notes", other)
	b := w.route(t)
	if got := pageRows(b); !slices.Equal(got, []string{"notes: Di", "strategy: Bo Cy Ann"}) {
		t.Fatalf("rows %q, want one row per doc with its threads newest first", got)
	}
	sync("strategy", []DocAsk{strategy[0], strategy[2]})
	b = w.route(t)
	if got := pageRows(b); !slices.Equal(got, []string{"notes: Di", "strategy: Cy Ann"}) {
		t.Fatalf("after Bo's thread resolved: rows %q", got)
	}
	for _, n := range b.Needs {
		if n.Doc != nil && n.Doc.Page == "strategy" {
			if err := w.core.Store.DismissQueue(n.DismissKeys()...); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := pageRows(w.route(t)); !slices.Equal(got, []string{"notes: Di"}) {
		t.Fatalf("a dismissed doc still shows: %q", got)
	}
	sync("strategy", []DocAsk{strategy[0]})
	if got := pageRows(w.route(t)); !slices.Equal(got, []string{"notes: Di"}) {
		t.Fatalf("a dismissed doc came back when one of its threads resolved: %q", got)
	}
	again := strategy[0]
	again.Version, again.At, again.Text = "d1@later", "2026-10-22T11:00:00Z", "still why now?"
	sync("strategy", []DocAsk{again})
	if got := pageRows(w.route(t)); !slices.Equal(got, []string{"notes: Di", "strategy: Ann"}) {
		t.Fatalf("a new comment on a dismissed doc: rows %q", got)
	}
}

func TestAManagerHearsOfEveryNewThreadOnADocInOneMessage(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "finish https://www.notion.so/55555555555555555555555555555555", "/repo/app")
	page := "55555555555555555555555555555555"
	var asks []DocAsk
	for i, who := range []string{"Ann", "Bo", "Cy"} {
		asks = append(asks, DocAsk{Key: "d" + who, Kind: "mention", Page: page, Title: "Spec", Asker: who, Text: who + " asks",
			Version: "v1", At: "2026-10-2" + string(rune('0'+i)) + "T00:00:00Z"})
	}
	if err := w.core.Store.SyncDocPage("notion", page, "e1", asks); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w.route(t)
	}
	msgs := w.handedTo(m.Session)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "3 thread(s)") || strings.Index(msgs[0], "Cy asks") > strings.Index(msgs[0], "Ann asks") {
		t.Fatalf("manager got %d messages, want one listing all three newest first: %q", len(msgs), msgs)
	}
}
