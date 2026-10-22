package core

import (
	"path/filepath"
	"testing"
)

func TestOpeningAStoreWithTheWorkLayerRetiresItOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	type fixture struct {
		handoff bool
		state   string
	}
	fixtures := []fixture{{true, ItemOpen}, {true, ItemOpen}, {true, ItemAnswered}, {true, ItemDismissed}, {true, ItemSnoozed}, {false, ItemOpen}, {false, ItemAnswered}, {false, ItemSnoozed}}
	var items []Item
	for i, f := range fixtures {
		key := ""
		if f.handoff {
			key = "claim-" + string(rune('a'+i))
		}
		it, _, err := s.addItem(Item{Scope: "repo", Session: "lead", Kind: KindDecision, Question: "Ship it?"}, key)
		if err != nil {
			t.Fatal(err)
		}
		if f.state != ItemOpen {
			if it, err = s.Transition(it.Key(), f.state, "fixture"); err != nil {
				t.Fatal(err)
			}
		}
		items = append(items, it)
	}
	for _, table := range retiredTables {
		if _, err = s.db.Exec(`CREATE TABLE ` + table + `(id TEXT)`); err != nil {
			t.Fatal(err)
		}
	}
	s.Close()

	for range 2 {
		if s, err = OpenStore(path); err != nil {
			t.Fatal(err)
		}
		for _, table := range retiredTables {
			var n int
			if err = s.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&n); err != nil || n != 0 {
				t.Fatalf("%s survived: %d %v", table, n, err)
			}
		}
		for i, f := range fixtures {
			got, err := s.Item(items[i].Key())
			if err != nil {
				t.Fatal(err)
			}
			retired := f.handoff && f.state == ItemOpen
			switch {
			case retired && (got.State != ItemDismissed || got.Reason != retiredHandoffReason || got.Delivered.IsZero() || len(got.History) != 2):
				t.Fatalf("open handoff item %d not dismissed silently: %+v", i, got)
			case !retired && (got.State != items[i].State || got.Reason != items[i].Reason || len(got.History) != len(items[i].History)):
				t.Fatalf("item %d changed: %+v, was %+v", i, got, items[i])
			}
		}
		s.Close()
	}
}
