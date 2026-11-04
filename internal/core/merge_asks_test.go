package core

import (
	"strings"
	"testing"
	"time"
)

const askedPR = "https://github.com/acme/repo/pull/5"

func askToMerge(t *testing.T, w *orgWorld, f func(*Item)) Item {
	t.Helper()
	w.spawn("asker", "")
	it := Item{Kind: KindChore, DoLine: "Merge the fix", Question: "Merge #5: fix it", Evidence: []string{askedPR}, Session: "asker"}
	if f != nil {
		f(&it)
	}
	added, err := w.core.AddOwnerItem(it)
	if err != nil {
		t.Fatal(err)
	}
	return added
}

func readyStatus(w *orgWorld) PRStatus {
	return PRStatus{State: "OPEN", Title: "fix it", Review: "APPROVED", Passed: 1, Fetched: w.clock,
		Merge: PRMergeFacts{Author: "me", Viewer: "me", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
}

func rowsOf(ns []Need, url string) []Need {
	var out []Need
	for _, n := range ns {
		if n.PR != nil && n.PR.URL == url || n.HeldPR != nil && n.HeldPR.URL == url {
			out = append(out, n)
		}
	}
	return out
}

func TestAnAskToMergeIsThatPullRequestsRowWhateverItsState(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status func(*PRStatus)
		// authored also lists the pull request the way the board's own merge rows find it.
		authored        bool
		verb, later     string
		live, checkMark bool
		itemStillOpen   bool
	}{
		{name: "ready and already on the board is one row", authored: true, verb: "mrg", live: true, checkMark: true, itemStillOpen: true},
		{name: "ready and requiring no review gets a row of its own", status: func(s *PRStatus) { s.Review = "" }, verb: "mrg", live: true, checkMark: true, itemStillOpen: true},
		{name: "wanting a review waits in later", status: func(s *PRStatus) { s.Review = "REVIEW_REQUIRED"; s.Merge.MergeState = "BLOCKED" }, verb: "mrg", later: "needs review", itemStillOpen: true},
		{name: "blocked waits in later", status: func(s *PRStatus) { s.Merge.MergeState = "BLOCKED" }, verb: "mrg", later: "blocked", itemStillOpen: true},
		{name: "failing waits in later", status: func(s *PRStatus) { s.Failed = 1 }, verb: "mrg", later: "CI failing", itemStillOpen: true},
		{name: "read too long ago waits in later", status: func(s *PRStatus) { s.Fetched = s.Fetched.Add(-2 * time.Hour) }, verb: "mrg", later: "read stale", itemStillOpen: true},
		{name: "closed unmerged waits in later", status: func(s *PRStatus) { s.State = "CLOSED" }, verb: "mrg", later: "closed", itemStillOpen: true},
		{name: "a teammate's never merges", status: func(s *PRStatus) { s.Merge.Author = "teammate" }, verb: "rev", live: true, itemStillOpen: true},
		{name: "merged closes itself", status: func(s *PRStatus) { s.State = "MERGED" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			it := askToMerge(t, w, nil)
			st := readyStatus(w)
			if tc.status != nil {
				tc.status(&st)
			}
			if err := w.core.Store.SavePRStatus(askedPR, st); err != nil {
				t.Fatal(err)
			}
			if tc.authored {
				if err := w.core.Store.SyncAuthored(map[string]string{askedPR: "fix it"}); err != nil {
					t.Fatal(err)
				}
			}
			b := w.board(t)
			live, later := rowsOf(b.Needs, askedPR), rowsOf(b.Later, askedPR)
			for _, n := range b.Needs {
				if n.Item != nil && n.Item.Seq == it.Seq && n.HeldPR == nil {
					t.Fatalf("the ask has a second row: %s", n.DoLine())
				}
			}
			switch {
			case tc.live:
				if len(live) != 1 || len(later) != 0 {
					t.Fatalf("live %d later %d, want one live row", len(live), len(later))
				}
				if v := live[0].Verb(); v != tc.verb {
					t.Fatalf("verb %s, want %s", v, tc.verb)
				}
				if (live[0].Check() == CheckMerge) != tc.checkMark {
					t.Fatalf("check %d, want a check mark %v", live[0].Check(), tc.checkMark)
				}
			case tc.later != "":
				if len(live) != 0 || len(later) != 1 {
					t.Fatalf("live %d later %d, want one later row", len(live), len(later))
				}
				if line, _ := RowLine(later[0], LaterLabel, QueueWidth, w.clock); !strings.Contains(line, tc.later) || later[0].Verb() != tc.verb {
					t.Fatalf("later row %q, want %s %q", line, tc.verb, tc.later)
				}
			default:
				if len(live)+len(later) != 0 {
					t.Fatalf("a merged pull request still shows: live %d later %d", len(live), len(later))
				}
			}
			if err := w.core.CloseMergedAsks(b); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			if (got.State == ItemOpen) != tc.itemStillOpen {
				t.Fatalf("item is %s, want open %v", got.State, tc.itemStillOpen)
			}
			if !tc.itemStillOpen && (got.State != ItemDismissed || !strings.Contains(got.Reason, "merged")) {
				t.Fatalf("item %s closed with %q, want a note saying it merged", got.State, got.Reason)
			}
		})
	}
}

func TestOnlyAnAskToMergeOneLinkedPullRequestIsThatPullRequest(t *testing.T) {
	for _, tc := range []struct {
		name string
		it   Item
		want bool
	}{
		{"merge with its link", Item{Kind: KindChore, Question: "Merge #5: fix", Evidence: []string{askedPR}}, true},
		{"review and merge", Item{Kind: KindChore, Question: "Review and merge #5", Evidence: []string{askedPR}}, true},
		{"a CI job beside the link", Item{Kind: KindChore, Question: "Merge #5", Evidence: []string{askedPR, "https://github.com/acme/repo/actions/runs/1/job/2"}}, true},
		{"into another's branch names the first", Item{Kind: KindChore, Question: "Merge #5 into #4's branch", Evidence: []string{askedPR}}, true},
		{"words without a link", Item{Kind: KindChore, Question: "Merge #5"}, false},
		{"a link to another pull request", Item{Kind: KindChore, Question: "Merge #6", Evidence: []string{askedPR}}, false},
		{"two linked pull requests", Item{Kind: KindChore, Question: "Merge these", Evidence: []string{askedPR, "https://github.com/acme/repo/pull/6"}}, false},
		{"a chore that only cites one", Item{Kind: KindChore, Question: "On the laptop, run ym update", Evidence: []string{askedPR}}, false},
		{"a word that starts the same", Item{Kind: KindChore, Question: "Mergers need a look", Evidence: []string{askedPR}}, false},
		{"a decision about merging", Item{Kind: KindDecision, Question: "Merge #5 now or later?", Evidence: []string{askedPR}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := MergeAskPR(tc.it); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAHeldAskComesBackAsAMergeRowByItself(t *testing.T) {
	w := newOrgWorld(t)
	askToMerge(t, w, nil)
	st := readyStatus(w)
	st.Review = "REVIEW_REQUIRED"
	if err := w.core.Store.SavePRStatus(askedPR, st); err != nil {
		t.Fatal(err)
	}
	if b := w.board(t); len(rowsOf(b.Later, askedPR)) != 1 {
		t.Fatalf("not held: later %d", len(b.Later))
	}
	st.Review = "APPROVED"
	if err := w.core.Store.SavePRStatus(askedPR, st); err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	rows := rowsOf(b.Needs, askedPR)
	if len(rows) != 1 || rows[0].Check() != CheckMerge || len(rowsOf(b.Later, askedPR)) != 0 {
		t.Fatalf("needs %d later %d", len(rows), len(rowsOf(b.Later, askedPR)))
	}
}

func TestAMergeRowRanksByItsAskAndMergingClosesIt(t *testing.T) {
	w := newOrgWorld(t)
	it := askToMerge(t, w, func(it *Item) { it.Blocks, it.Impact = 2, ImpactProd })
	if err := w.core.Store.SavePRStatus(askedPR, readyStatus(w)); err != nil {
		t.Fatal(err)
	}
	f := &fakeMerger{}
	w.core.PRs = f
	b := w.board(t)
	rows := rowsOf(b.Needs, askedPR)
	if len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
	plain := Need{PR: rows[0].PR}
	if !rows[0].Urgent() || rows[0].Score(b.At)-plain.Score(b.At) != 2*20+50 {
		t.Fatalf("urgent %v, score %d against %d", rows[0].Urgent(), rows[0].Score(b.At), plain.Score(b.At))
	}
	if _, err := w.core.DoCheck(b, rows[0]); err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Store.Item(it.Key())
	if err != nil {
		t.Fatal(err)
	}
	if got.State != ItemDismissed || len(f.merged) != 1 {
		t.Fatalf("item %s, merged %v", got.State, f.merged)
	}
	b = w.board(t)
	if n := len(rowsOf(b.Needs, askedPR)) + len(rowsOf(b.Later, askedPR)); n != 0 {
		t.Fatalf("%d rows after the merge", n)
	}
}

func TestAPageShowsAnotherHostsRowsAsTheBoardDoesAndActsOnlyWhereItRoutes(t *testing.T) {
	f := newFleet(t)
	f.a.spawn("asker", "")
	it, err := f.a.core.AddOwnerItem(Item{Kind: KindGo, Question: "ship it?", DoLine: "Ship it", Rec: "yes", Session: "asker"})
	if err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	f.sync(t)
	ids := func(ns []Need) []string {
		var out []string
		for _, n := range ns {
			out = append(out, n.DoLine())
		}
		return out
	}
	b := f.b.board(t)
	page, err := f.b.core.WithHostRows(b)
	if err != nil {
		t.Fatal(err)
	}
	var row *Need
	for i := range page.Needs {
		if n := &page.Needs[i]; n.Item != nil && n.Item.Seq == it.Seq && n.Item.Host == "a" {
			row = n
		}
	}
	if row == nil {
		t.Fatalf("no row from host a: %v", ids(page.Needs))
	}
	if len(b.Needs) >= len(page.Needs) {
		t.Fatal("the page added the row to the board it was given")
	}
	f.b.core.ShowRemotes = true
	shown := f.b.board(t)
	f.b.core.ShowRemotes = false
	if got, want := strings.Join(ids(page.Needs), "\n"), strings.Join(ids(shown.Needs), "\n"); got != want {
		t.Fatalf("page rows:\n%s\nboard rows:\n%s", got, want)
	}
	if row.Check() != CheckGo {
		t.Fatalf("a row this host routes to has check %d", row.Check())
	}
	if _, err := f.b.core.DoCheck(page, *row); err != nil {
		t.Fatal(err)
	}
	pending, err := f.b.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Dest != "a" || !strings.Contains(pending[0].Body, "item") || !strings.Contains(pending[0].Body, it.Key()) {
		t.Fatalf("the check mark did not head for host a: %+v", pending)
	}
	stranded := Need{Item: row.Item, NoAct: true}
	if stranded.Check() != CheckNone || stranded.Accept() != CheckNone {
		t.Fatalf("a row with no route has check %d accept %d", stranded.Check(), stranded.Accept())
	}
	if line, _ := RowLine(stranded, "1", QueueWidth, f.b.clock); !strings.HasSuffix(line, "▸") {
		t.Fatalf("row %q", line)
	}
}

func TestAnAskBoundForTheOwnerNeedsADoLine(t *testing.T) {
	for _, tc := range []struct {
		name string
		it   Item
		ok   bool
	}{
		{"a chore without one", Item{Kind: KindChore, Question: "log in"}, false},
		{"a chore with one", Item{Kind: KindChore, Question: "log in", DoLine: "Log in"}, true},
		{"a decision without one", Item{Kind: KindDecision, Question: "q", Rec: "r", Options: picks}, false},
		{"a decision with one", Item{Kind: KindDecision, Question: "q", Rec: "r", Options: picks, DoLine: "Pick r"}, true},
		{"a go needs none", Item{Kind: KindGo, Question: "q", Rec: "r"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.spawn("asker", "")
			tc.it.Session = "asker"
			_, err := w.core.AddOwnerItem(tc.it)
			if (err == nil) != tc.ok {
				t.Fatalf("err %v, want ok %v", err, tc.ok)
			}
			if err != nil && !strings.Contains(err.Error(), "--do") {
				t.Fatalf("refusal %q does not say what to add", err)
			}
		})
	}
}
