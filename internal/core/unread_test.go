package core

import (
	"errors"
	"strings"
	"testing"
)

func TestASessionInATmuxYmCannotReadIsNeverOutsideOrGone(t *testing.T) {
	for _, tc := range []struct{ name, err string }{
		{"tmux killed", "tmux -L ym list-panes: signal: killed"},
		{"tmux missing", `exec: "tmux": executable file not found in $PATH`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			ym := &fakeHost{name: "ym", ttys: map[string]string{}, screens: map[string]string{}, ours: true, readErr: errors.New(tc.err)}
			w.core.Hosts = append(w.core.Hosts, ym)
			var fronted []string
			w.core.Front = func(tty string) error { fronted = append(fronted, tty); return nil }
			w.run(100, 1, "", "/opt/homebrew/bin/tmux", "", Idle)
			w.run(101, 100, "/dev/ttys011", "claude", "inside-ym", Idle)
			b := w.board(t)
			s, ok := b.Session("inside-ym")
			if !ok {
				t.Fatal("the session left the board")
			}
			found, err := w.core.Find("inside-ym")
			if err != nil {
				t.Fatal(err)
			}
			for _, l := range []Live{s, found} {
				if l.HostErr == "" || l.Muxed || l.Adoptable() || !strings.Contains(l.Unadoptable(), "can't read ym's tmux") {
					t.Fatalf("live %+v: want the unread host named, never outside or adoptable", l)
				}
			}
			ts := w.core.Targets(b, "inside-ym", "claude")
			if len(ts) != 1 || ts[0].Resume != nil {
				t.Fatalf("targets %+v, want one with no resume command", ts)
			}
			advice := ts[0].Advice()
			if !strings.Contains(advice, "can't read ym's tmux: "+tc.err) || !strings.Contains(advice, "repair") || strings.Contains(advice, "--resume") {
				t.Fatalf("advice %q, want the read error and repair, never a command line", advice)
			}
			if err := w.core.Focus(ts[0]); !errors.Is(err, ErrNoHost) || len(fronted) != 0 || len(ym.focused)+len(w.tmux.focused) != 0 {
				t.Fatalf("focus = %v, fronted %v: want nothing moved", err, fronted)
			}
		})
	}
}

func TestOpeningASessionInATerminalYmDoesNotOwnBringsThatTerminalForward(t *testing.T) {
	for _, tc := range []struct {
		name, tty, outer string
		front            error
		fronted          string
		advice           bool
	}{
		{name: "terminal app tab", tty: "/dev/ttys004", fronted: "/dev/ttys004"},
		{name: "terminal app that no longer shows it", tty: "/dev/ttys004", front: ErrNoHost, fronted: "/dev/ttys004", advice: true},
		{name: "owner's tmux in a terminal app", tty: "/dev/ttys007", outer: "/dev/ttys002", fronted: "/dev/ttys002"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			w.tmux.ttys["/dev/ttys007"] = "%7"
			w.tmux.outer = tc.outer
			var fronted []string
			w.core.Front = func(tty string) error { fronted = append(fronted, tty); return tc.front }
			w.run(200, 1, "", "/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", "", Idle)
			w.run(201, 200, tc.tty, "claude", "s1", Idle)
			ts := w.core.Targets(w.board(t), "s1", "claude")
			if len(ts) != 1 {
				t.Fatalf("targets %+v", ts)
			}
			err := w.core.Focus(ts[0])
			if strings.Join(fronted, " ") != tc.fronted || errors.Is(err, ErrNoHost) != tc.advice {
				t.Fatalf("fronted %v, focus %v; want %s fronted, advice %v", fronted, err, tc.fronted, tc.advice)
			}
			if advice := ts[0].Advice(); strings.Contains(advice, "--resume") || strings.Contains(advice, "claude ") {
				t.Fatalf("advice %q prints a command line", advice)
			}
		})
	}
}

func TestASessionYmLaunchedIsNeverOutsideEvenWhenItsPaneIsNotFound(t *testing.T) {
	w := newWorld(t)
	w.core.Launches = &fakeLaunches{w: w, launched: []Launched{{Key: "repo-wt-x", Harness: "claude", Session: "launched", State: "running"}}}
	var fronted []string
	w.core.Front = func(tty string) error { fronted = append(fronted, tty); return nil }
	w.run(200, 1, "", "/Applications/iTerm.app/Contents/MacOS/iTerm2", "", Idle)
	w.run(201, 200, "/dev/ttys011", "claude", "launched", Idle)
	b := w.board(t)
	s, _ := b.Session("launched")
	ts := w.core.Targets(b, "launched", "claude")
	if !s.Ours || s.Adoptable() || len(ts) != 1 {
		t.Fatalf("live %+v, targets %+v: want it ym's, not adoptable", s, ts)
	}
	if err := w.core.Focus(ts[0]); !errors.Is(err, ErrNoHost) || len(fronted) != 0 {
		t.Fatalf("focus %v fronted %v, want no other terminal touched", err, fronted)
	}
	if advice := ts[0].Advice(); !strings.Contains(advice, "repair") || strings.Contains(advice, "--resume") {
		t.Fatalf("advice %q", advice)
	}
}
