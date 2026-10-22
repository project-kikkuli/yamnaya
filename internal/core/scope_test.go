package core

import (
	"slices"
	"testing"
)

func TestAScopedBoardShowsOnlySessionsUnderItsDirectory(t *testing.T) {
	for _, c := range []struct {
		scope string
		want  []string
	}{
		{"", []string{"inside", "nested", "outside", "sibling"}},
		{"/runs/one", []string{"inside", "nested"}},
		{"/runs/one/", []string{"inside", "nested"}},
	} {
		t.Run(c.scope, func(t *testing.T) {
			w := newWorld(t)
			for id, cwd := range map[string]string{"inside": "/runs/one", "nested": "/runs/one/repo-wt-task", "sibling": "/runs/one-more", "outside": "/home/me/app"} {
				w.spawn(id, "")
				w.set(id, func(s *Session) { s.Cwd = cwd })
			}
			w.core.Scope = c.scope
			var got []string
			for _, s := range w.board(t).Sessions {
				got = append(got, s.ID)
			}
			slices.Sort(got)
			if !slices.Equal(got, c.want) {
				t.Fatalf("scope %q shows %v, want %v", c.scope, got, c.want)
			}
		})
	}
}
