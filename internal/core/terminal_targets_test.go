package core

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"testing"
)

func TestTerminalTargetsRetainHostProfileAndPhysicalCopies(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/local1", "claude", "shared", Busy)
	w.run(11, 1, "/dev/local2", "claude", "shared", Busy)
	w.run(12, 1, "/dev/work", "claude", "shared", Busy)
	work := w.claude.sessions[12]
	work.Profile = "work"
	w.claude.sessions[12] = work
	r := RemoteBoard{Host: "box", Sessions: []Live{
		{Session: Session{ID: "shared", Harness: "claude", PID: 20, TTY: "/dev/remote1"}},
		{Session: Session{ID: "shared", Harness: "claude", PID: 21, TTY: "/dev/remote2"}},
		{Session: Session{ID: "shared", Harness: "claude", Profile: "work", PID: 30, TTY: "/dev/remote-work"}},
	}}
	w.core.Remotes, w.core.ShowRemotes = &fakeRemotes{boards: []RemoteBoard{r}}, true
	b := w.board(t)
	for _, reverse := range []bool{false, true} {
		rows := slices.Clone(b.Sessions)
		if reverse {
			slices.Reverse(rows)
		}
		board := Board{Sessions: rows}
		for _, tc := range []struct {
			ref, host, profile string
			pids               []int
		}{
			{"shared", "", "", nil},
			{"claude:default@shared", "", "", []int{10, 11}},
			{"claude:work@shared", "", "work", []int{12}},
			{"box/claude:default@shared", "box", "", []int{20, 21}},
			{"box/claude:work@shared", "box", "work", []int{30}},
			{"missing/claude:default@shared", "missing", "", nil},
			{"box/claude:default@absent", "box", "", nil},
		} {
			t.Run(tc.ref+"/reverse="+strconv.FormatBool(reverse), func(t *testing.T) {
				targets := w.core.Targets(board, tc.ref, "claude")
				var pids []int
				for i, target := range targets {
					if target.Remote != tc.host || target.Session.Profile != tc.profile {
						t.Fatalf("target crossed host/profile: %+v", target)
					}
					pids = append(pids, target.Session.PID)
					if target.Remote == "" {
						continue
					}
					cmd, err := w.core.AttachCmd(target)
					want := []string{"ssh", "-t", "box", "open", "--no-board", target.Session.ExactRef()}
					if len(targets) > 1 {
						want = append(want, "--pick", strconv.Itoa(i+1))
					}
					if err != nil || cmd == nil || !reflect.DeepEqual(cmd.Args, want) {
						t.Fatalf("remote copy lost selection: command=%v error=%v want=%v", cmd, err, want)
					}
				}
				slices.Sort(pids)
				if !reflect.DeepEqual(pids, tc.pids) {
					t.Fatalf("physical copies=%v want=%v", pids, tc.pids)
				}
			})
		}
	}
}

func TestAbsentRemoteTransportCannotOpenALocalTerminal(t *testing.T) {
	w := newWorld(t)
	fronted := false
	w.core.Front = func(string) error { fronted = true; return nil }
	target := Target{Remote: "box", Session: Session{Harness: "claude", ID: "shared", TTY: "/dev/local1"}}
	if cmd, err := w.core.AttachCmd(target); cmd != nil || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("missing remote transport returned a local attach: %v, %v", cmd, err)
	}
	if err := w.core.Focus(target); !errors.Is(err, ErrNoHost) || fronted {
		t.Fatalf("remote target focused local terminal: %v, fronted=%v", err, fronted)
	}
	if err := w.core.OpenBeside(target); !errors.Is(err, ErrNoHost) {
		t.Fatalf("remote target opened beside local: %v", err)
	}
}
