package iterm

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type call struct {
	script string
	args   []string
}

func fake(running bool, listing string) (*Host, *[]call) {
	var calls []call
	h := &Host{
		Running: func() bool { return running },
		Run: func(script string, args ...string) (string, error) {
			calls = append(calls, call{script, args})
			if script == listScript {
				return listing, nil
			}
			return "ok", nil
		},
	}
	return h, &calls
}

func TestLocateNeverAsksAnITermThatIsNotRunning(t *testing.T) {
	h, calls := fake(false, "")
	if _, ok := h.Locate("/dev/ttys004"); ok || len(*calls) != 0 {
		t.Fatalf("ok %v, calls %v", ok, *calls)
	}
}

func TestLocateFocusTypeAndScreenBySessionID(t *testing.T) {
	h, calls := fake(true, "/dev/ttys004\tw0t0p0:AAAA\t1001\t1\n/dev/ttys007\tw0t1p0:BBBB\t1001\t2\n")
	hd, ok := h.Locate("/dev/ttys007")
	if !ok || hd != (core.Handle{Host: Name, ID: "w0t1p0:BBBB", Label: "window 1001 tab 2"}) {
		t.Fatalf("Locate = %+v, %v", hd, ok)
	}
	h.Locate("/dev/ttys004")
	if len(*calls) != 1 {
		t.Fatalf("listing ran %d times for two lookups", len(*calls))
	}
	outer, err := h.Focus(hd)
	if err != nil || outer != "" {
		t.Fatal(outer, err)
	}
	if err := h.Type(hd, "re D2: yes"); err != nil {
		t.Fatal(err)
	}
	h.Screen(hd)
	want := []call{{focusScript, []string{hd.ID}}, {typeScript, []string{hd.ID, "re D2: yes"}}, {screenScript, []string{hd.ID}}}
	for i, c := range want {
		got := (*calls)[i+1]
		if got.script != c.script || len(got.args) != len(c.args) || got.args[len(got.args)-1] != c.args[len(c.args)-1] {
			t.Errorf("call %d = %+v, want %+v", i, got.args, c.args)
		}
	}
}
