package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEachStateDirectoryKeepsItsServeRunningUnderItsOwnUnits(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	def := filepath.Join(home, ".local", "state", "yamnaya")
	unit := func(dir string) string {
		t.Helper()
		u, err := serveUnit(dir)
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	for _, tc := range []struct {
		name string
		a, b string
		same bool
	}{
		{name: "the default state directory, however it is spelled", a: def, b: def + "/", same: true},
		{name: "a rehearsal beside the owner's ym", a: def, b: "/tmp/rehearsal/state"},
		{name: "two rehearsals", a: "/tmp/run1/state", b: "/tmp/run2/state"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ua, ub := unit(tc.a), unit(tc.b)
			if (ua == ub) != tc.same {
				t.Errorf("%s keeps ym serve as %s and %s as %s; same units: %v, want %v", tc.a, ua, tc.b, ub, ua == ub, tc.same)
			}
		})
	}
	if u := unit(def); u != "ym-serve" {
		t.Errorf("the owner's ym serve is kept by %s, want the ym-serve units it has always had", u)
	}
}
