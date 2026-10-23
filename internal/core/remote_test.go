package core

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

type fakeRemotes struct {
	boards []RemoteBoard
	ran    [][]string
	out    string
	reads  int
}

func (f *fakeRemotes) Hosts() []string {
	var out []string
	for _, b := range f.boards {
		out = append(out, b.Host)
	}
	return out
}
func (f *fakeRemotes) Boards() []RemoteBoard { f.reads++; return f.boards }
func (f *fakeRemotes) Run(host string, args ...string) (string, error) {
	f.ran = append(f.ran, append([]string{host}, args...))
	return f.out, nil
}
func (f *fakeRemotes) Attach(host string, args ...string) *exec.Cmd {
	return exec.Command("ssh", append([]string{"-t", host}, args...)...)
}

func remoteWorld(t *testing.T) (*world, *fakeRemotes) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "local-lead", Busy)
	r := &fakeRemotes{boards: []RemoteBoard{
		{Host: "box", At: w.core.now(), Sessions: []Live{
			{Session: Session{Harness: "claude", ID: "remote-lead-0001", PID: 10, Title: "fix a typo", Status: Waiting}, Project: "remote-lead-0001", Remote: "box", Where: "ym %3"},
			{Session: Session{Harness: "codex", ID: "remote-crew-0002", PID: 11, Status: Busy}, Project: "remote-lead-0001", Remote: "box", Where: "ym %4"},
		}, Items: []Item{{Seq: 1, ID: "D1", Kind: KindDecision, Question: "ship it?", Session: "remote-crew-0002", State: ItemOpen, Host: "box"}},
			Waiting: []RemoteWaiting{{Session: "remote-lead-0001", Line: "which branch?", Reply: "which branch?"}}},
		{Host: "gone", Err: errors.New("ssh: Could not resolve hostname gone")},
	}}
	w.core.Remotes, w.core.ShowRemotes = r, true
	return w, r
}

func TestRemoteSessionsJoinTheBoardUnderTheirHost(t *testing.T) {
	w, _ := remoteWorld(t)
	b := w.board(t)
	got := projectKeys(b)
	if want := []string{"remote-lead-0001", "remote-crew-0002"}; !reflect.DeepEqual(got["remote-lead-0001"], want) || len(got["local-lead"]) != 1 {
		t.Fatalf("projects = %v", got)
	}
	s, _ := b.Session("remote-lead-0001")
	if s.Location() != "box ym %3" {
		t.Errorf("location = %q", s.Location())
	}
	var asks []string
	for _, n := range b.Needs {
		switch {
		case n.Item != nil:
			asks = append(asks, "item "+n.Item.Key()+" "+n.Project)
		case n.Waiting():
			asks = append(asks, "waiting "+n.Session.Key())
		}
	}
	if want := []string{"item box/#1 remote-lead-0001", "waiting remote-lead-0001"}; !reflect.DeepEqual(asks, want) {
		t.Errorf("needs = %v, want %v", asks, want)
	}
	if len(b.Remotes) != 2 || !strings.HasPrefix(b.Remotes[1].State(), "unreachable: ") {
		t.Errorf("remotes = %+v", b.Remotes)
	}
}

func TestActionsOnRemoteSessionsRunOnTheirHost(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(c *Core, b Board) error
		want []string
	}{
		{"kill", func(c *Core, b Board) error { s, _ := b.Session("remote-lead-0001"); _, err := c.Kill(s); return err },
			[]string{"box", "kill", "--yes", "remote-lead-0001"}},
		{"answer", func(c *Core, b Board) error { _, _, err := c.Answer(b, "box/#1", "yes"); return err },
			[]string{"box", "item", "answer", "#1", "yes"}},
		{"dismiss", func(c *Core, b Board) error { _, _, err := c.Dismiss(b, "box/#1", "moot"); return err },
			[]string{"box", "item", "dismiss", "#1", "moot"}},
		{"snooze", func(c *Core, b Board) error { _, err := c.Snooze("box/#1"); return err },
			[]string{"box", "item", "snooze", "#1"}},
		{"archive", func(c *Core, b Board) error { return c.StopSession("launch:box/fix-typo") },
			[]string{"box", "kill", "--yes", "fix-typo"}},
		{"relaunch", func(c *Core, b Board) error {
			s, _ := b.Session("remote-crew-0002")
			_, err := c.Relaunch(s, "claude")
			return err
		},
			[]string{"box", "switch", "remote-crew-0002", "--to", "claude", "--relaunch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := remoteWorld(t)
			b := w.board(t)
			if err := tc.act(w.core, b); err != nil {
				t.Fatal(err)
			}
			if len(r.ran) != 1 || !reflect.DeepEqual(r.ran[0], tc.want) {
				t.Errorf("ran %v, want %v", r.ran, tc.want)
			}
		})
	}
}

func TestOpeningARemoteSessionAttachesOverSSH(t *testing.T) {
	w, _ := remoteWorld(t)
	b := w.board(t)
	ts := w.core.Targets(b, "remote-lead-0001", "claude")
	if len(ts) != 1 || ts[0].Remote != "box" {
		t.Fatalf("targets = %+v", ts)
	}
	cmd := w.core.AttachCmd(ts[0])
	if want := []string{"ssh", "-t", "box", "open", "--no-board", "remote-lead-0001"}; cmd == nil || !reflect.DeepEqual(cmd.Args, want) {
		t.Errorf("attach = %v, want %v", cmd, want)
	}
	if s, err := w.core.Find("remote-lead"); err != nil || s.Remote != "box" {
		t.Errorf("Find = %+v, %v", s, err)
	}
}

func TestLocalRefsNeverReadRemoteHosts(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(c *Core, b Board)
	}{
		{"answer", func(c *Core, b Board) { c.Answer(b, "#99", "x") }},
		{"snooze", func(c *Core, b Board) { c.Snooze("D99") }},
		{"archive", func(c *Core, b Board) { c.StopSession("launch:local-key") }},
		{"find", func(c *Core, b Board) { c.Find("local-lead") }},
		{"qualified", func(c *Core, b Board) { c.RemoteRef("box/#1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := remoteWorld(t)
			w.core.ShowRemotes = false
			b := w.board(t)
			tc.act(w.core, b)
			if r.reads != 0 || len(r.ran) != 0 {
				t.Errorf("read remote boards %d times and ran %v for a local ref", r.reads, r.ran)
			}
		})
	}
}

func TestEveryItemListsAnotherHostsOpenAndLatelyClosedItemsWithTheirHistory(t *testing.T) {
	w, r := remoteWorld(t)
	note := Event{At: w.core.now(), From: ItemAnswered, To: ItemAnswered, Text: "ym item answer #2 C from a did not land: answered there already: D"}
	r.boards[0].Closed = []Item{{Seq: 2, ID: "D2", Kind: KindDecision, Question: "which word?", State: ItemAnswered, Answer: "D", Host: "box",
		History: []Event{{At: w.core.now(), To: ItemOpen}, {At: w.core.now(), From: ItemOpen, To: ItemAnswered, Text: "D"}, note}}}
	b := w.board(t)
	for _, tc := range []struct {
		all  bool
		want []string
	}{{all: false, want: []string{"box/#1"}}, {all: true, want: []string{"box/#1", "box/#2"}}} {
		items, err := w.core.ItemsIn(b, "", tc.all)
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, it := range items {
			if it.Host != "" {
				keys = append(keys, it.Key())
			}
		}
		if !reflect.DeepEqual(keys, tc.want) {
			t.Errorf("all=%v: remote items %v, want %v", tc.all, keys, tc.want)
		}
		if tc.all && !strings.Contains(items[len(items)-1].History[2].Text, "did not land") {
			t.Errorf("box/#2 history %+v lacks the refused answer's note", items[len(items)-1].History)
		}
	}
}
