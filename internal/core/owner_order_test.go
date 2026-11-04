package core

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"
	"time"
)

type orderWorld struct {
	*orgWorld
	items map[string]Item
}

func newOrderWorld(t *testing.T) orderWorld {
	t.Helper()
	w := orderWorld{newOrgWorld(t), map[string]Item{}}
	add := func(name string, blocks int, minutes int) {
		w.spawn("s-"+name, "")
		it, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: name, DoLine: name, Rec: "x", Blocks: blocks, Minutes: minutes, Session: "s-" + name})
		if err != nil {
			t.Fatal(err)
		}
		w.items[name] = it
	}
	add("owner-pin", 0, 1)
	add("chief-one", 0, 1)
	add("chief-two", 1, 1)
	add("high", 3, 5)
	add("mid", 2, 5)
	add("low", 1, 5)
	add("least", 0, 5)
	if _, _, err := w.core.Store.RankItem(w.items["owner-pin"].Key(), "top", true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"chief-one", "chief-two"} {
		if _, _, err := w.core.Store.RankItem(w.items[name].Key(), "top", false); err != nil {
			t.Fatal(err)
		}
	}
	url := "https://github.com/acme/repo/pull/9"
	st := readyStatus(w.orgWorld)
	if err := w.core.Store.SyncAuthored(map[string]string{url: "pr"}); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.SavePRStatus(url, st); err != nil {
		t.Fatal(err)
	}
	return w
}

func orderOf(ns []Need) []string {
	out := make([]string, len(ns))
	for i, n := range ns {
		out[i] = n.ID()
	}
	return out
}

func TestTheOwnersUpAndDownMoveExactlyOnePlace(t *testing.T) {
	for _, seed := range []int64{1, 2, 3, 4} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			w := newOrderWorld(t)
			r := rand.New(rand.NewSource(seed))
			for step := range 40 {
				b := w.board(t)
				model := orderOf(b.Needs)
				at := r.Intn(len(model))
				dir, other := "up", at-1
				if r.Intn(2) == 0 {
					dir, other = "down", at+1
				}
				if err := w.core.RankNeed(b, b.Needs[at], dir); err != nil {
					t.Fatal(err)
				}
				if other >= 0 && other < len(model) {
					model[at], model[other] = model[other], model[at]
				}
				got := orderOf(w.board(t).Needs)
				if !slices.Equal(got, model) {
					t.Fatalf("step %d: %s %s\n got %v\nwant %v", step, b.Needs[at].ID(), dir, got, model)
				}
			}
		})
	}
}

func TestAnOwnersStepsKeepHoldingTheRowAgainstAChief(t *testing.T) {
	for _, tc := range []struct {
		name  string
		row   string
		steps []string
	}{
		{"up on a scored row", "low", []string{"up"}},
		{"down on a scored row", "mid", []string{"down"}},
		{"up then down, back where it was", "low", []string{"up", "down"}},
		{"down then up", "mid", []string{"down", "up"}},
		{"up on a row a chief pinned", "chief-two", []string{"up"}},
		{"down on a row a chief pinned", "chief-one", []string{"down", "down"}},
		{"up at the very top changes no place", "owner-pin", []string{"up"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrderWorld(t)
			key := w.items[tc.row].Key()
			for _, dir := range tc.steps {
				b := w.board(t)
				i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.Key() == key })
				if err := w.core.RankNeed(b, b.Needs[i], dir); err != nil {
					t.Fatal(err)
				}
			}
			before := orderOf(w.board(t).Needs)
			for _, dir := range []string{"top", "up", "down"} {
				if _, ok, err := w.core.Store.RankItem(key, dir, false); err != nil || ok {
					t.Fatalf("a chief's %s took: ok %v, %v", dir, ok, err)
				}
			}
			if got := orderOf(w.board(t).Needs); !slices.Equal(got, before) {
				t.Fatalf("a chief moved a held row:\n got %v\nwant %v", got, before)
			}
		})
	}
}

func TestAChiefsStepOnARowTheOwnerLeftStaysAsItWas(t *testing.T) {
	for _, tc := range []struct {
		dir string
		pin int
	}{{"up", 1}, {"down", -1}} {
		t.Run(tc.dir, func(t *testing.T) {
			w := newOrderWorld(t)
			b := w.board(t)
			i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.DoLine == "high" })
			if err := w.core.RankNeed(b, b.Needs[i], "up"); err != nil {
				t.Fatal(err)
			}
			it, ok, err := w.core.Store.RankItem(w.items["least"].Key(), tc.dir, false)
			if err != nil || !ok || it.Pin != tc.pin || it.PinOwner {
				t.Fatalf("pin %d owner %v ok %v, %v; want a chief's pin %d", it.Pin, it.PinOwner, ok, err, tc.pin)
			}
		})
	}
}

func TestTopStaysTopAfterSteps(t *testing.T) {
	w := newOrderWorld(t)
	for _, name := range []string{"low", "mid", "low"} {
		b := w.board(t)
		i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.DoLine == name })
		if err := w.core.RankNeed(b, b.Needs[i], "up"); err != nil {
			t.Fatal(err)
		}
	}
	b := w.board(t)
	i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.DoLine == "least" })
	if err := w.core.RankNeed(b, b.Needs[i], "top"); err != nil {
		t.Fatal(err)
	}
	got := w.board(t).Needs
	if got[0].Item == nil || got[0].Item.DoLine != "least" {
		t.Fatalf("top went to %v", orderOf(got))
	}
	if err := w.core.RankNeed(w.board(t), got[0], "down"); err != nil {
		t.Fatal(err)
	}
	if err := w.core.RankNeed(w.board(t), w.board(t).Needs[1], "up"); err != nil {
		t.Fatal(err)
	}
	if n := w.board(t).Needs[0]; n.Item == nil || n.Item.DoLine != "least" {
		t.Fatalf("down then up did not bring it back: %v", orderOf(w.board(t).Needs))
	}
}

func TestAStepLeavesScoreToRankEverythingElse(t *testing.T) {
	w := newOrderWorld(t)
	b := w.board(t)
	i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.DoLine == "low" })
	passed := b.Needs[i-1].ID()
	if err := w.core.RankNeed(b, b.Needs[i], "up"); err != nil {
		t.Fatal(err)
	}
	w.spawn("s-urgent", "")
	if _, err := w.core.AddOwnerItem(Item{Kind: KindGo, Question: "urgent", DoLine: "urgent", Rec: "x", Impact: ImpactProd, Blocks: 2, Session: "s-urgent"}); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Minute)
	got := w.board(t).Needs
	pos := func(name string) int {
		return slices.IndexFunc(got, func(n Need) bool { return n.Item != nil && n.Item.DoLine == name })
	}
	if pos("urgent") > pos("high") {
		t.Fatalf("a new prod row did not outrank by score: %v", orderOf(got))
	}
	if at := slices.Index(orderOf(got), passed); pos("low") != at-1 {
		t.Fatalf("low no longer sits just above %s: %v", passed, orderOf(got))
	}
}
