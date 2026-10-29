package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// TestABoardOverUnchangedInputsStillShowsEveryWrite reloads a board between writes to its database, from this ym and from
// another process's connection, and expects each write on the very next board, however soon.
func TestABoardOverUnchangedInputsStillShowsEveryWrite(t *testing.T) {
	for _, writer := range []string{"same store", "another process"} {
		t.Run(writer, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym.db")
			st, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			w := st
			if writer == "another process" {
				if w, err = OpenStore(path); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { w.Close() })
			}
			now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
			c := &Core{Store: st, Now: func() time.Time { return now }, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			for i := range 3 {
				b, err := c.Board()
				if err != nil {
					t.Fatal(err)
				}
				if len(ownerItems(b)) != i {
					t.Fatalf("board %d shows %d open items, want %d", i, len(ownerItems(b)), i)
				}
				if _, err := w.AddItem(Item{Kind: "decision", Question: "Ship it?", Session: "gone", Recipient: Owner}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
