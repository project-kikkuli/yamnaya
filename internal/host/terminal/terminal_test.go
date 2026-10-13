package terminal

import "testing"

func TestLocateByTabTTYOnlyWhenTerminalRuns(t *testing.T) {
	calls := 0
	h := &Host{
		Running: func() bool { return false },
		Run: func(script string, args ...string) (string, error) {
			calls++
			return "/dev/ttys000\t42\t1\n/dev/ttys008\t42\t2\n", nil
		},
	}
	if _, ok := h.Locate("/dev/ttys008"); ok || calls != 0 {
		t.Fatalf("asked a Terminal that is not running: ok %v, calls %d", ok, calls)
	}
	h.Running, h.ttys = func() bool { return true }, nil
	hd, ok := h.Locate("/dev/ttys008")
	if !ok || hd.ID != "/dev/ttys008" || hd.Label != "window 42 tab 2" {
		t.Fatalf("Locate = %+v, %v", hd, ok)
	}
	var got []string
	h.Run = func(script string, args ...string) (string, error) {
		got = args
		return "", nil
	}
	if _, err := h.Focus(hd); err != nil || len(got) != 1 || got[0] != "/dev/ttys008" {
		t.Fatalf("Focus args %v, err %v", got, err)
	}
}
