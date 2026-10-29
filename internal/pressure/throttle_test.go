package pressure

import (
	"errors"
	"os"
	"reflect"
	"sort"
	"syscall"
	"testing"
	"time"
)

type signals struct {
	sent  []string
	fail  map[int]error
	forks map[int]Process
	procs []Process
}

func (s *signals) list() ([]Process, error) { return s.procs, nil }

func (s *signals) signal(p Process, sig syscall.Signal) error {
	if err := s.fail[p.PID]; err != nil {
		return err
	}
	name := "CONT "
	if sig == syscall.SIGSTOP {
		name = "STOP "
	}
	s.sent = append(s.sent, name+p.Command)
	if child, ok := s.forks[p.PID]; ok && sig == syscall.SIGSTOP {
		s.procs = append(s.procs, child)
		delete(s.forks, p.PID)
	}
	return nil
}

func tree() []Process {
	return []Process{
		{PID: 10, PPID: 1, Command: "claude", Started: 1},
		{PID: 11, PPID: 10, Command: "zsh", Started: 1},
		{PID: 12, PPID: 11, Command: "go", Started: 1},
		{PID: 13, PPID: 10, Command: "ym", Started: 1},
		{PID: 14, PPID: 13, Command: "ym-child", Started: 1},
		{PID: 20, PPID: 1, Command: "codex", Started: 1},
		{PID: 21, PPID: 20, Command: "node", Started: 1},
		{PID: 30, PPID: 1, Command: "Finder", Started: 1},
	}
}

func TestPauseStopsEachAgentTreeParentsFirstAndSparesYm(t *testing.T) {
	for _, tc := range []struct {
		name    string
		roots   map[int]string
		fail    map[int]error
		forks   map[int]Process
		want    []string
		paused  []string
		failure bool
	}{
		{"every agent", map[int]string{10: "a", 20: "b"}, nil, nil,
			[]string{"STOP claude", "STOP zsh", "STOP go", "STOP codex", "STOP node"}, []string{"a", "b"}, false},
		{"one agent", map[int]string{20: "b"}, nil, nil,
			[]string{"STOP codex", "STOP node"}, []string{"b"}, false},
		{"a child forked mid-pause", map[int]string{20: "b"}, nil, map[int]Process{21: {PID: 22, PPID: 21, Command: "cargo", Started: 1}},
			[]string{"STOP codex", "STOP node", "STOP cargo"}, []string{"b"}, false},
		{"a process already gone", map[int]string{10: "a"}, map[int]error{11: os.ErrProcessDone}, nil,
			[]string{"STOP claude"}, []string{"a"}, false},
		{"a process ym may not signal", map[int]string{20: "b"}, map[int]error{21: syscall.EPERM}, nil,
			[]string{"STOP codex"}, []string{"b"}, true},
		{"a root that already exited", map[int]string{99: "gone"}, nil, nil, nil, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &signals{procs: tree(), fail: tc.fail, forks: tc.forks}
			var th Throttle
			at := time.Unix(1000, 0)
			_, err := th.Pause(at, s.list, tc.roots, s.signal)
			if (err != nil) != tc.failure {
				t.Fatalf("err %v, want failure %v", err, tc.failure)
			}
			if !reflect.DeepEqual(s.sent, tc.want) {
				t.Fatalf("sent %q, want %q", s.sent, tc.want)
			}
			if got := th.Sessions(); !reflect.DeepEqual(got, tc.paused) {
				t.Fatalf("paused sessions %q, want %q", got, tc.paused)
			}
			if th.On() != (len(tc.want) > 0) || th.On() && !th.Since.Equal(at) {
				t.Fatalf("on %v since %v after %d stops", th.On(), th.Since, len(tc.want))
			}
		})
	}
}

func TestResumeContinuesTheChosenAgentsChildrenFirstAndKeepsWhatItCouldNotReach(t *testing.T) {
	for _, tc := range []struct {
		name      string
		in        func(string) bool
		fail      map[int]error
		want      []string
		remaining []string
	}{
		{"all", func(string) bool { return true }, nil,
			[]string{"CONT node", "CONT codex", "CONT go", "CONT zsh", "CONT claude"}, nil},
		{"one agent", func(s string) bool { return s == "a" }, nil,
			[]string{"CONT go", "CONT zsh", "CONT claude"}, []string{"b"}},
		{"an exited process is forgotten", func(string) bool { return true }, map[int]error{21: os.ErrProcessDone},
			[]string{"CONT codex", "CONT go", "CONT zsh", "CONT claude"}, nil},
		{"an unreachable process stays paused", func(string) bool { return true }, map[int]error{21: syscall.EPERM},
			[]string{"CONT codex", "CONT go", "CONT zsh", "CONT claude"}, []string{"b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &signals{procs: tree()}
			var th Throttle
			if _, err := th.Pause(time.Unix(1000, 0), s.list, map[int]string{10: "a", 20: "b"}, s.signal); err != nil {
				t.Fatal(err)
			}
			s.sent, s.fail = nil, tc.fail
			at := time.Unix(2000, 0)
			_, err := th.Resume(at, tc.in, s.signal)
			if errors.Is(tc.fail[21], syscall.EPERM) != (err != nil) {
				t.Fatalf("err %v with failures %v", err, tc.fail)
			}
			if !reflect.DeepEqual(s.sent, tc.want) {
				t.Fatalf("sent %q, want %q", s.sent, tc.want)
			}
			got := th.Sessions()
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.remaining) {
				t.Fatalf("still paused %q, want %q", got, tc.remaining)
			}
			if !th.Ended.Equal(at) || th.On() == th.Since.IsZero() {
				t.Fatalf("ended %v since %v on %v", th.Ended, th.Since, th.On())
			}
		})
	}
}

func TestThrottleSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	want := Throttle{Since: time.Unix(1000, 0).UTC(), Paused: []Paused{{PID: 10, Started: 1, Command: "claude", Session: "a"}}}
	if err := want.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := ReadThrottle(dir)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("read %+v, %v; want %+v", got, err, want)
	}
}
