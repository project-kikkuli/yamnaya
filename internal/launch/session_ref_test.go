package launch

import (
	"errors"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestLaunchReferencesNeverSelectAnAmbiguousSession(t *testing.T) {
	for _, ids := range [][2]string{
		{"01a16a70-a4c4-7670-a0a2-e092c1f5e145", "01a16a70-a4c4-7670-a0a2-e0a21f96395f"},
		{"61ffadaf-e067-418c-bf5a-71518e8fba10", "61ffadaf-e067-418c-bf5a-71518e8fba11"},
	} {
		for _, landed := range []bool{false, true} {
			r := Records{Dir: t.TempDir()}
			for i, id := range ids {
				rec := Record{Key: AdoptKey(id), Session: id}
				if landed && i == 0 {
					rec.Landed = time.Now()
				}
				if err := r.Save(rec); err != nil {
					t.Fatal(err)
				}
			}
			if all, _ := r.All(); len(all) != 2 {
				t.Fatalf("adopted records overwrote one another: %+v", all)
			}
			for _, id := range ids {
				for _, ref := range []string{id, AdoptKey(id)} {
					if rec, err := r.Lookup(ref); err != nil || rec.Session != id {
						t.Fatalf("Lookup(%s) = %+v, %v", ref, rec, err)
					}
				}
			}
			if _, err := r.Lookup(ids[0][:8]); !errors.Is(err, core.ErrAmbiguous) {
				t.Fatalf("shared prefix selected a launch: %v", err)
			}
			if rec, ok := r.Find(ids[0][:8]); ok {
				t.Fatalf("Find selected ambiguous launch: %+v", rec)
			}
			if ids[0][14] == '7' {
				for _, id := range ids {
					if rec, err := r.Lookup(core.Short(id)); err != nil || rec.Session != id {
						t.Fatalf("displayed shorthand = %+v, %v", rec, err)
					}
				}
			}
		}
	}
}
