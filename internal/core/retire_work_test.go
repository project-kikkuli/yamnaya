package core

import (
	"path/filepath"
	"testing"
)

func TestOpeningAStoreRetiresTheWorkLayerAndTheHandoffClassifierOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range append(append([]string{}, retiredTables...), handoffTables...) {
		if _, err = s.db.Exec(`CREATE TABLE ` + table + `(id TEXT, item INTEGER)`); err != nil {
			t.Fatal(err)
		}
	}
	type fixture struct {
		handoff bool
		state   string
	}
	fixtures := []fixture{{true, ItemOpen}, {true, ItemOpen}, {true, ItemAnswered}, {true, ItemDismissed}, {true, ItemSnoozed}, {false, ItemOpen}, {false, ItemAnswered}, {false, ItemSnoozed}}
	var items []Item
	for _, f := range fixtures {
		it, err := s.AddItem(Item{Scope: "repo", Session: "lead", Kind: KindDecision, Question: "Ship it?"})
		if err != nil {
			t.Fatal(err)
		}
		if f.handoff {
			if _, err = s.db.Exec(`INSERT INTO ym_handoff_items(id, item) VALUES (?, ?)`, it.Key(), it.Seq); err != nil {
				t.Fatal(err)
			}
		}
		if f.state != ItemOpen {
			if it, err = s.Transition(it.Key(), f.state, "fixture"); err != nil {
				t.Fatal(err)
			}
		}
		items = append(items, it)
	}
	s.Close()

	for range 2 {
		if s, err = OpenStore(path); err != nil {
			t.Fatal(err)
		}
		for _, table := range append(append([]string{}, retiredTables...), handoffTables...) {
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
			retired := f.handoff && (f.state == ItemOpen || f.state == ItemSnoozed)
			switch {
			case retired && (got.State != ItemDismissed || got.Reason != retiredHandoffReason || got.Delivered.IsZero() || len(got.History) != len(items[i].History)+1):
				t.Fatalf("pending handoff item %d not dismissed silently: %+v", i, got)
			case !retired && (got.State != items[i].State || got.Reason != items[i].Reason || len(got.History) != len(items[i].History)):
				t.Fatalf("item %d changed: %+v, was %+v", i, got, items[i])
			}
		}
		s.Close()
	}
}

func TestOpenHandoffItemsOfOwnerSessionsBecomeTheirWaitingRowsOnce(t *testing.T) {
	const message = "The worker is done.\n\nOpen for you\n4. Keep the retry wrapper or switch?\n\nNext: answer item 4."
	path := filepath.Join(t.TempDir(), "ym.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	open, err := st.AddItem(Item{Scope: "repo", Session: "mine", Kind: KindDecision, Question: "Answer item 4"})
	if err != nil {
		t.Fatal(err)
	}
	answered, err := st.AddItem(Item{Scope: "repo", Session: "mine", Kind: KindDecision, Question: "Old one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.Transition(answered.Key(), ItemAnswered, "keep"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`DROP TABLE ym_session_asks`, nil},
		{`CREATE TABLE ym_handoff_items (id TEXT PRIMARY KEY, item INTEGER NOT NULL)`, nil},
		{`CREATE TABLE ym_handoff_claims (id TEXT PRIMARY KEY, work TEXT NOT NULL, claim BLOB NOT NULL)`, nil},
		{`CREATE TABLE ym_handoff_targets (id TEXT PRIMARY KEY, target TEXT NOT NULL)`, nil},
		{`INSERT INTO ym_handoff_items (id, item) VALUES ('k1', ?), ('k2', ?)`, []any{open.Seq, answered.Seq}},
		{`INSERT INTO ym_handoff_claims (id, work, claim) VALUES ('c1', 'mine', json_object('Event', json_object('Text', ?)))`, []any{message}},
		{`INSERT INTO ym_handoff_targets (id, target) VALUES ('c1', ?)`, []any{"item:" + open.Key()}},
	} {
		if _, err = st.db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	for range 2 {
		if st, err = OpenStore(path); err != nil {
			t.Fatal(err)
		}
		got, _ := st.Item(open.Key())
		old, _ := st.Item(answered.Key())
		asks, _ := st.asking()
		if got.State != ItemDismissed || got.Delivered.IsZero() || old.State != ItemAnswered || old.Answer != "keep" || asks["mine"] != message {
			t.Fatalf("open %s delivered %v, answered %s %q, waiting %q", got.State, got.Delivered, old.State, old.Answer, asks["mine"])
		}
		st.Close()
	}
}
