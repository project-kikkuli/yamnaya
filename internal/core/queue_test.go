package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

var queueNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

func queueItem(q string, f func(*Item)) Need {
	it := Item{ID: q, Kind: KindDecision, Question: q, CreatedAt: queueNow}
	if f != nil {
		f(&it)
	}
	return Need{Item: &it}
}

func ownedPR(n int) Need {
	st := PRStatus{State: "OPEN", Title: fmt.Sprintf("pr%d", n), Review: "APPROVED", Passed: 1, Merge: PRMergeFacts{Author: "me", Viewer: "me", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	return Need{PR: &PR{URL: fmt.Sprintf("https://github.com/acme/repo/pull/%d", n), Repo: "acme/repo", Number: n, Status: &st}}
}

func oldPR(n int) Need {
	need := ownedPR(n)
	need.PR.Status.Merge.Opened = queueNow.Add(-48 * time.Hour)
	return need
}

func teammatePR(n int) Need {
	need := ownedPR(n)
	need.PR.Status.Merge.Author = "teammate"
	return need
}

func TestRankOrdersEveryKindOfRowByOneScore(t *testing.T) {
	pinned := func(q string, pin int, owner bool) Need {
		return queueItem(q, func(it *Item) { it.Pin, it.PinOwner = pin, owner })
	}
	for _, tc := range []struct {
		name string
		rows []Need
		want string
	}{
		{"prod beats what blocks one piece of work", []Need{
			queueItem("blocks", func(it *Item) { it.Blocks = 1 }),
			queueItem("prod", func(it *Item) { it.Impact = ImpactProd }),
		}, "prod,blocks"},
		{"clinical ranks with prod", []Need{
			queueItem("routine", func(it *Item) { it.Impact = ImpactRoutine }),
			queueItem("clinical", func(it *Item) { it.Impact = ImpactClinical }),
		}, "clinical,routine"},
		{"each blocked piece counts", []Need{
			queueItem("prod", func(it *Item) { it.Impact = ImpactProd }),
			queueItem("three", func(it *Item) { it.Blocks = 3 }),
		}, "three,prod"},
		{"a merge-ready pull request beats a plain decision", []Need{queueItem("plain", nil), ownedPR(1)}, "repo#1,plain"},
		{"prod beats a plain merge row at its highest", []Need{oldPR(1), queueItem("prod", func(it *Item) { it.Impact = ImpactProd })}, "prod,repo#1"},
		{"clinical beats a plain merge row at its highest", []Need{oldPR(1), queueItem("clinical", func(it *Item) { it.Impact = ImpactClinical })}, "clinical,repo#1"},
		{"age counts by the hour", []Need{
			queueItem("new", nil),
			queueItem("old", func(it *Item) { it.CreatedAt = queueNow.Add(-5 * time.Hour) }),
		}, "old,new"},
		{"age stops at a day", []Need{
			queueItem("week", func(it *Item) { it.CreatedAt = queueNow.Add(-7 * 24 * time.Hour) }),
			queueItem("blocks", func(it *Item) { it.Blocks = 2 }),
		}, "blocks,week"},
		{"a quick win passes an equal row", []Need{
			queueItem("slow", nil),
			queueItem("quick", func(it *Item) { it.Minutes = 2 }),
		}, "quick,slow"},
		{"a tie goes to the shorter", []Need{
			queueItem("twenty", func(it *Item) { it.Minutes = 20 }),
			queueItem("five", func(it *Item) { it.Minutes = 5 }),
		}, "five,twenty"},
		{"a tie puts an unknown size last", []Need{
			queueItem("unknown", func(it *Item) { it.Kind = KindChore }),
			queueItem("known", nil),
		}, "known,unknown"},
		{"a large review sinks under a quick go", []Need{
			{Review: &ReviewRequest{URL: "https://github.com/acme/august/pull/19634", Additions: 6252, Deletions: 39373}},
			queueItem("go", func(it *Item) { it.Kind = KindGo }),
		}, "go,august#19634"},
		{"a pin overrides any score", []Need{
			queueItem("prod", func(it *Item) { it.Impact = ImpactProd; it.Blocks = 5 }),
			pinned("pinned", 1, false),
		}, "pinned,prod"},
		{"a higher pin goes first", []Need{pinned("one", 1, false), pinned("two", 2, false)}, "two,one"},
		{"the owner's pin beats a higher chief pin", []Need{pinned("chief", 9, false), pinned("owner", 1, true)}, "owner,chief"},
		{"a pin down goes under every score", []Need{pinned("down", -1, false), queueItem("plain", nil)}, "plain,down"},
		{"the owner's pin down goes under a chief's", []Need{pinned("owner", -1, true), pinned("chief", -3, false)}, "chief,owner"},
		{"a row that is not an item takes pins too", []Need{
			queueItem("prod", func(it *Item) { it.Impact = ImpactProd }),
			{Review: &ReviewRequest{URL: "https://github.com/acme/repo/pull/2"}, Mark: QueueMark{Pin: 1, Owner: true}},
		}, "repo#2,prod"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rank(tc.rows, queueNow)
			var got []string
			for _, n := range tc.rows {
				got = append(got, n.ID())
			}
			if s := strings.Join(got, ","); s != tc.want {
				t.Fatalf("order %s, want %s", s, tc.want)
			}
		})
	}
}

func TestARowSaysWhatItsCheckMarkDoes(t *testing.T) {
	for _, tc := range []struct {
		name          string
		need          Need
		verb, min     string
		check, accept Check
	}{
		{"my pull request on the board merges", ownedPR(1), "mrg", "1m", CheckMerge, CheckMerge},
		{"a teammate's pull request never merges", teammatePR(3), "rev", "?m", CheckNone, CheckNone},
		{"a review request opens", Need{Review: &ReviewRequest{URL: "https://github.com/a/b/pull/4", Additions: 100}}, "rev", "10m", CheckNone, CheckNone},
		{"a go item goes", queueItem("q", func(it *Item) { it.Kind = KindGo }), "go", "1m", CheckGo, CheckGo},
		{"a decision whose do-line states the rec is accepted", queueItem("q", func(it *Item) { it.DoLine, it.Rec = "use CAS", "use CAS" }), "dec", "3m", CheckAccept, CheckAccept},
		{"a decision without a do-line opens before it can be accepted", queueItem("q", func(it *Item) { it.Rec = "use CAS" }), "dec", "3m", CheckNone, CheckAccept},
		{"a decision without a rec has nothing to accept", queueItem("q", func(it *Item) { it.DoLine = "decide" }), "dec", "3m", CheckNone, CheckNone},
		{"a chore needs hands", queueItem("q", func(it *Item) { it.Kind, it.DoLine, it.Rec = KindChore, "log in", "x" }), "do", "?m", CheckNone, CheckNone},
		{"a chat action opens its chat", queueItem("q", func(it *Item) { it.Kind, it.Action = KindGo, "chat" }), "cht", "1m", CheckNone, CheckNone},
		{"a stuck goal opens", Need{Goal: &Goal{ID: "goal1", Ask: "x"}}, "stk", "?m", CheckNone, CheckNone},
		{"a session on its first-run setup opens", Need{Session: &Live{Session: Session{Status: Onboarding}}}, "set", "1m", CheckNone, CheckNone},
		{"a permission prompt opens", Need{Session: &Live{Session: Session{Status: Waiting}}}, "prm", "1m", CheckNone, CheckNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if v := tc.need.Verb(); v != tc.verb {
				t.Errorf("verb %s, want %s", v, tc.verb)
			}
			if m := QueueMinutes(tc.need.Minutes()); m != tc.min {
				t.Errorf("minutes %s, want %s", m, tc.min)
			}
			if c := tc.need.Check(); c != tc.check {
				t.Errorf("check %d, want %d", c, tc.check)
			}
			if c := tc.need.Accept(); c != tc.accept {
				t.Errorf("accept %d, want %d", c, tc.accept)
			}
			line, _ := RowLine(tc.need, "1", QueueWidth, queueNow)
			mark := "▸"
			if tc.check != CheckNone {
				mark = "✓"
			}
			if !strings.HasSuffix(line, mark) {
				t.Errorf("row %q does not end in %s", line, mark)
			}
		})
	}
}

func TestAHugeDiffGetsAHonestEstimateAndAnUnknownOneNoNumber(t *testing.T) {
	huge := Need{Review: &ReviewRequest{URL: "https://github.com/acme/august/pull/19634", Additions: 6252, Deletions: 39373}}
	if m := huge.Minutes(); m < 8*60 {
		t.Fatalf("august#19634 estimated at %d minutes", m)
	}
	unknown := Need{Review: &ReviewRequest{URL: "https://github.com/acme/august/pull/1"}}
	go1 := queueItem("go", func(it *Item) { it.Kind = KindGo })
	if line, _ := RowLine(unknown, "1", QueueWidth, queueNow); !strings.Contains(line, " ?m ") {
		t.Fatalf("unknown size row %q", line)
	}
	if h := QueueHeader([]Need{unknown, go1}, 0); h != "NEXT 2 · ~1m" {
		t.Fatalf("header %q leaves unknown minutes out", h)
	}
	if h := QueueHeader([]Need{unknown}, 2); !strings.HasPrefix(h, "NEXT 1 ") || !strings.HasSuffix(h, "later 2 ▾") || ansi.StringWidth(h) != QueueWidth {
		t.Fatalf("header %q", h)
	}
}

func TestARowIsOneLineAndGainsDimFactsOnlyWhenWide(t *testing.T) {
	long := strings.Repeat("a very long question ", 10)
	for _, tc := range []struct {
		name string
		need Need
	}{
		{"question cut to fit", queueItem(long, nil)},
		{"full do-line and urgent", queueItem(long, func(it *Item) { it.DoLine, it.Impact = strings.Repeat("x", MaxDoLine), ImpactProd })},
		{"long title", Need{Review: &ReviewRequest{URL: "https://github.com/acme/vibe-apps/pull/3384", Title: long}}},
	} {
		for _, rankLabel := range []string{"1", "12"} {
			t.Run(tc.name+" rank "+rankLabel, func(t *testing.T) {
				line, side := RowLine(tc.need, rankLabel, QueueWidth, queueNow)
				if ansi.StringWidth(line) != QueueWidth || strings.Contains(line, "\n") || side != "" {
					t.Fatalf("at 40 columns %q side %q", line, side)
				}
				if do := tc.need.DoLine(); len(do) <= MaxDoLine && !strings.Contains(line, do) {
					t.Fatalf("a do-line that fits was cut: %q", line)
				}
				wide, side := RowLine(tc.need, rankLabel, 120, queueNow)
				if wide != line || side == "" || ansi.StringWidth(wide+side) > 120 {
					t.Fatalf("at 120 columns %q side %q", wide, side)
				}
				if !strings.Contains(side, tc.need.ID()[:4]) {
					t.Fatalf("side %q lacks the id", side)
				}
			})
		}
	}
}

const stayRanked = time.Duration(-1)

func endedGoalAsk(impact, snooze string) func(*testing.T, *orgWorld) string {
	return func(t *testing.T, w *orgWorld) string {
		g, m := w.goal(t, "ship", "/repo/a")
		it, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: "done goal", Rec: "x", Impact: impact, Session: m.Session})
		if err != nil {
			t.Fatal(err)
		}
		g.State = GoalDone
		if err := w.core.Store.SaveGoal(g); err != nil {
			t.Fatal(err)
		}
		if snooze != "" {
			if _, err := w.core.SnoozeFor(it.Key(), snooze); err != nil {
				t.Fatal(err)
			}
		}
		return it.Qualified()
	}
}

func TestLaterFoldsSnoozedRowsAndEndedGoalsAndSnoozesComeBackOnTheirOwn(t *testing.T) {
	url := "https://github.com/acme/repo/pull/5"
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, w *orgWorld) string
		// back is what passes the snooze; zero means the row stays in later.
		back time.Duration
	}{
		{"an item for an hour", func(t *testing.T, w *orgWorld) string {
			it := ownerAsk(t, w, "one hour")
			if _, err := w.core.SnoozeFor(it.Key(), "1h"); err != nil {
				t.Fatal(err)
			}
			return it.Qualified()
		}, time.Hour + time.Minute},
		{"an item until tomorrow", func(t *testing.T, w *orgWorld) string {
			it := ownerAsk(t, w, "tomorrow")
			if _, err := w.core.SnoozeFor(it.Key(), "tomorrow"); err != nil {
				t.Fatal(err)
			}
			return it.Qualified()
		}, 25 * time.Hour},
		{"an item whose session has no live project", func(t *testing.T, w *orgWorld) string {
			it, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: "gone", Rec: "x", Session: "gone-session"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.SnoozeFor(it.Key(), "1h"); err != nil {
				t.Fatal(err)
			}
			return it.Qualified()
		}, time.Hour + time.Minute},
		{"an item until reopened stays", func(t *testing.T, w *orgWorld) string {
			it := ownerAsk(t, w, "until reopened")
			if _, err := w.core.Snooze(it.Key()); err != nil {
				t.Fatal(err)
			}
			return it.Qualified()
		}, 0},
		{"a review row", func(t *testing.T, w *orgWorld) string {
			if err := w.core.Store.SyncReviews([]ReviewRequest{{URL: url, Updated: "2026-10-20T00:00:00Z", Title: "t", Author: "a"}}); err != nil {
				t.Fatal(err)
			}
			if err := w.core.SnoozeNeed(Need{Review: &ReviewRequest{URL: url}}, "tonight"); err != nil {
				t.Fatal(err)
			}
			return "repo#5"
		}, 25 * time.Hour},
		{"a merge row, through a new head", func(t *testing.T, w *orgWorld) string {
			st := *ownedPR(5).PR.Status
			st.Fetched = w.clock.Add(2 * time.Hour)
			if err := w.core.Store.SyncAuthored(map[string]string{url: "t"}); err != nil {
				t.Fatal(err)
			}
			if err := w.core.Store.SavePRStatus(url, st); err != nil {
				t.Fatal(err)
			}
			if err := w.core.SnoozeNeed(Need{PR: &PR{URL: url}}, "1h"); err != nil {
				t.Fatal(err)
			}
			st.Merge.OID = "newhead"
			if err := w.core.Store.SavePRStatus(url, st); err != nil {
				t.Fatal(err)
			}
			return "repo#5"
		}, 2 * time.Hour},
		{"an item of a finished goal stays", func(t *testing.T, w *orgWorld) string {
			g, m := w.goal(t, "ship", "/repo/a")
			it, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: "done goal", Rec: "x", Session: m.Session})
			if err != nil {
				t.Fatal(err)
			}
			g.State = GoalDone
			if err := w.core.Store.SaveGoal(g); err != nil {
				t.Fatal(err)
			}
			return it.Qualified()
		}, 0},
		{"a prod item of a finished goal stays ranked", endedGoalAsk(ImpactProd, ""), stayRanked},
		{"a clinical item of a finished goal stays ranked", endedGoalAsk(ImpactClinical, ""), stayRanked},
		{"a routine item of a finished goal folds", endedGoalAsk(ImpactRoutine, ""), 0},
		{"a snoozed prod item of a finished goal folds until it wakes", endedGoalAsk(ImpactProd, "1h"), time.Hour + time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			id := tc.setup(t, w)
			has := func(ns []Need) bool {
				for _, n := range ns {
					if n.ID() == id {
						return true
					}
				}
				return false
			}
			b := w.board(t)
			if tc.back == stayRanked {
				if !has(b.Needs) || has(b.Later) {
					t.Fatalf("%s not ranked: needs %d later %d", id, len(b.Needs), len(b.Later))
				}
				return
			}
			if has(b.Needs) || !has(b.Later) {
				t.Fatalf("%s not folded: needs %d later %d", id, len(b.Needs), len(b.Later))
			}
			if tc.back == 0 {
				return
			}
			w.clock = w.clock.Add(tc.back)
			b = w.board(t)
			if !has(b.Needs) || has(b.Later) {
				t.Fatalf("%s did not come back after %s", id, tc.back)
			}
		})
	}
}

func ownerAsk(t *testing.T, w *orgWorld, q string) Item {
	t.Helper()
	w.spawn("asker", "")
	it, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: q, Rec: "x", Session: "asker"})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func TestSnoozeUntilReadsTheLocalClock(t *testing.T) {
	loc := time.Local
	for _, tc := range []struct {
		when string
		at   time.Time
		want time.Time
	}{
		{"1h", time.Date(2026, 11, 3, 10, 0, 0, 0, loc), time.Date(2026, 11, 3, 11, 0, 0, 0, loc)},
		{"tonight", time.Date(2026, 11, 3, 10, 0, 0, 0, loc), time.Date(2026, 11, 3, 20, 0, 0, 0, loc)},
		{"t", time.Date(2026, 11, 3, 22, 0, 0, 0, loc), time.Date(2026, 11, 4, 20, 0, 0, 0, loc)},
		{"tomorrow", time.Date(2026, 11, 3, 22, 0, 0, 0, loc), time.Date(2026, 11, 4, 9, 0, 0, 0, loc)},
		{"m", time.Date(2026, 11, 3, 1, 0, 0, 0, loc), time.Date(2026, 11, 4, 9, 0, 0, 0, loc)},
	} {
		t.Run(tc.when, func(t *testing.T) {
			got, err := SnoozeUntil(tc.at, tc.when, nil)
			if err != nil || !got.Equal(tc.want) {
				t.Fatalf("%s from %s = %s, %v; want %s", tc.when, tc.at, got, err, tc.want)
			}
		})
	}
	if _, err := SnoozeUntil(queueNow, "someday", nil); err == nil {
		t.Fatal("someday is not a time")
	}
}

func TestTheOwnersRankStandsOverAChiefsAndAChiefsPinNeverWakesASnooze(t *testing.T) {
	w := newOrgWorld(t)
	a, c := ownerAsk(t, w, "a"), ownerAsk(t, w, "c")
	st := w.core.Store
	if it, ok, err := st.RankItem(a.Key(), "top", false); err != nil || !ok || it.Pin != 1 || it.PinOwner {
		t.Fatalf("chief top = %+v %v %v", it, ok, err)
	}
	if it, ok, err := st.RankItem(a.Key(), "down", true); err != nil || !ok || it.Pin != -1 || !it.PinOwner {
		t.Fatalf("owner down of a chief's pin = pin %d owner %v, %v %v", it.Pin, it.PinOwner, ok, err)
	}
	if it, ok, err := st.RankItem(a.Key(), "top", false); err != nil || ok || it.Pin != -1 {
		t.Fatalf("chief rank over the owner's = pin %d %v %v", it.Pin, ok, err)
	}
	if _, err := w.core.SnoozeFor(c.Key(), "1h"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.RankItem(c.Key(), "top", false); err != nil || !ok {
		t.Fatal(ok, err)
	}
	b := w.board(t)
	for _, n := range b.Needs {
		if n.Item != nil && n.Item.Seq == c.Seq {
			t.Fatal("a chief's pin brought a snoozed item back")
		}
	}
	if _, _, err := st.RankItem(a.Key(), "sideways", true); err == nil {
		t.Fatal("sideways is not a rank")
	}
}

func TestAddAndPrepareRefuseWhatARowCannotShow(t *testing.T) {
	w := newOrgWorld(t)
	w.spawn("asker", "")
	base := Item{Kind: KindGo, Question: "q", Rec: "r", Session: "asker"}
	for _, tc := range []struct {
		name string
		f    func(*Item)
		ok   bool
	}{
		{"28 characters", func(it *Item) { it.DoLine = strings.Repeat("é", MaxDoLine) }, true},
		{"29 characters", func(it *Item) { it.DoLine = strings.Repeat("x", MaxDoLine+1) }, false},
		{"known impact", func(it *Item) { it.Impact = ImpactClinical }, true},
		{"unknown impact", func(it *Item) { it.Impact = "huge" }, false},
		{"negative minutes", func(it *Item) { it.Minutes = -1 }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := base
			tc.f(&it)
			got, err := w.core.AddItem(it)
			if (err == nil) != tc.ok {
				t.Fatalf("err %v, want ok %v", err, tc.ok)
			}
			if tc.ok && (got.DoLine != it.DoLine || got.Impact != it.Impact) {
				t.Fatalf("lost fields: %+v", got)
			}
		})
	}
}

type fakeMerger struct {
	PRSource
	merged []string
	err    error
}

func (f *fakeMerger) Merge(url, head string) error {
	f.merged = append(f.merged, url+"@"+head)
	return f.err
}

func TestTheCheckMarkOnlyMergesTheOwnersPullRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		need   Need
		merged int
		err    error
	}{
		{"mine", ownedPR(1), 1, nil},
		{"a teammate's", teammatePR(3), 0, nil},
		{"GitHub refuses", ownedPR(4), 1, errors.New("head moved")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			f := &fakeMerger{err: tc.err}
			w.core.PRs = f
			_, err := w.core.DoCheck(Board{}, tc.need)
			if len(f.merged) != tc.merged {
				t.Fatalf("merged %v, want %d", f.merged, tc.merged)
			}
			if tc.merged == 1 && tc.err == nil {
				if err != nil || !strings.HasSuffix(f.merged[0], "@abc") {
					t.Fatalf("merged %v err %v", f.merged, err)
				}
			} else if err == nil {
				t.Fatal("a check mark that did nothing reported no error")
			}
		})
	}
}

func TestTheCheckMarkAnswersThroughTheItemPaths(t *testing.T) {
	for _, tc := range []struct {
		name, answer string
		item         Item
	}{
		{"go", GoAhead, Item{Kind: KindGo, Question: "build?", Rec: "yes"}},
		{"a check mark answers yes to the words on the row", "Yes: Use CAS", Item{Kind: KindDecision, Question: "store?", Rec: "a long unread rec", DoLine: "Use CAS", Options: picks}},
		{"a default's too", "Yes: Use CAS", Item{Kind: KindDefault, Question: "store?", Rec: "a long unread rec", DoLine: "Use CAS"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.spawn("asker", "")
			tc.item.Session = "asker"
			it, err := w.core.AddOwnerItem(tc.item)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.DoCheck(w.board(t), Need{Item: &it}); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Item(it.Key())
			if err != nil || got.State != ItemAnswered || got.Answer != tc.answer {
				t.Fatalf("item %+v, %v", got, err)
			}
		})
	}
}

func TestSnoozeUntilReadsTheClockItIsGiven(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	tokyo, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Skip(err)
	}
	at := time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, when string
		loc        *time.Location
		want       time.Time
	}{
		{"tonight in New York", "tonight", ny, time.Date(2026, 11, 4, 20, 0, 0, 0, ny)},
		{"tomorrow in New York", "tomorrow", ny, time.Date(2026, 11, 5, 9, 0, 0, 0, ny)},
		{"past 8pm in Tokyo, tonight is the next", "tonight", tokyo, time.Date(2026, 11, 5, 20, 0, 0, 0, tokyo)},
		{"tomorrow in Tokyo", "tomorrow", tokyo, time.Date(2026, 11, 5, 9, 0, 0, 0, tokyo)},
		{"an hour is an hour anywhere", "1h", tokyo, at.Add(time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SnoozeUntil(at, tc.when, tc.loc)
			if err != nil || !got.Equal(tc.want) {
				t.Fatalf("got %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}

func TestAnotherHostIsToldHowLongASnoozeOnTheBrowsersClockLasts(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip(err)
	}
	f := newFleet(t)
	f.a.spawn("asker", "")
	it, err := f.a.core.AddOwnerItem(Item{Kind: KindGo, Question: "ship it?", Rec: "yes", Session: "asker"})
	if err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	f.sync(t)
	it.Host = "a"
	f.b.clock = time.Date(2026, 11, 4, 14, 0, 0, 0, time.UTC)
	if err := f.b.core.SnoozeNeedIn(Need{Item: &it}, "tonight", ny); err != nil {
		t.Fatal(err)
	}
	pending, err := f.b.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 4, 20, 0, 0, 0, ny).Sub(f.b.clock).String()
	if len(pending) != 1 || !strings.Contains(pending[0].Body, want) || strings.Contains(pending[0].Body, "tonight") {
		t.Fatalf("relayed %+v, want the duration %s", pending, want)
	}
}

func TestReRankingShowsAtOnceWithoutAScan(t *testing.T) {
	w := newOrgWorld(t)
	w.spawn("a", "")
	w.spawn("b", "")
	high, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: "first", Rec: "x", Blocks: 3, Session: "a"})
	if err != nil {
		t.Fatal(err)
	}
	low, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: "second", Rec: "x", Session: "b"})
	if err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	if b.Needs[0].Item.Seq != high.Seq {
		t.Fatalf("order before: %d first", b.Needs[0].Item.Seq)
	}
	i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.Seq == low.Seq })
	if err := w.core.RankNeed(b, b.Needs[i], "top"); err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Reranked(b, b.Needs[i])
	if err != nil {
		t.Fatal(err)
	}
	if got.Needs[0].Item.Seq != low.Seq || !got.Needs[0].mark().Owner {
		t.Fatalf("after: %+v", got.Needs[0].Item)
	}
	if b.Needs[0].Item.Seq != high.Seq {
		t.Fatal("the board given was changed")
	}
}
