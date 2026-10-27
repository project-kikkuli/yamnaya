package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const fakeBoard = `{"sessions":[{"harness":"claude","id":"aaaa-1111","pid":7,"status":"busy","title":"fix one typo","host":"ym %2","project":"aaaa-1111","repo_key":"/src/app/.git"}],
"items":[{"seq":4,"id":"D2","kind":"decision","state":"open","question":"merge?","session":"aaaa-1111","scope":"/src/app/.git","recipient":""}],
"launched":[{"key":"fix-one-typo","harness":"claude","session":"aaaa-1111","state":"running","path":"/src/app-wt"}],
"dormant":[],"waiting":[]}`

// fakeSSH is an ssh that answers per host: a board, an unreachable host, or one that never answers.
func fakeSSH(t *testing.T) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "board.json"), []byte(fakeBoard), 0o644); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
while [ "$1" = -o ] || [ "$1" = -t ]; do [ "$1" = -o ] && shift; shift; done
case "$1" in
box) cat ` + filepath.Join(dir, "board.json") + ` ;;
dead) echo "ssh: Could not resolve hostname dead: nodename nor servname provided" >&2; exit 255 ;;
slow) exec sleep 30 ;;
esac
`
	bin := filepath.Join(dir, "ssh")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestRemoteHostsReadEveryHostAtOnceAndNameTheOnesThatFail(t *testing.T) {
	r := newRemoteHosts([]string{"box", "dead", "slow"}, 5*time.Second, nil)
	r.ssh, r.timeout = fakeSSH(t), 2*time.Second
	start := time.Now()
	boards := r.Boards()
	if took := time.Since(start); took > 4*time.Second {
		t.Errorf("Boards took %s; hosts should be read at once and bounded by the timeout", took)
	}
	states := map[string]string{}
	for _, b := range boards {
		states[b.Host] = b.State()
	}
	for host, want := range map[string]string{"box": "1 sessions", "dead": "Could not resolve hostname dead", "slow": "no answer in 2s"} {
		if !strings.Contains(states[host], want) {
			t.Errorf("%s: state %q, want it to say %q", host, states[host], want)
		}
	}
	box := boards[0]
	s := box.Sessions[0]
	if s.Remote != "box" || s.Location() != "box ym %2" || s.RepoKey != "box//src/app/.git" {
		t.Errorf("session = %+v", s)
	}
	if box.Items[0].Key() != "box/#4" || box.Launched[0].Key != "box/fix-one-typo" || box.Launched[0].Session != "aaaa-1111" {
		t.Errorf("item %q, launch %+v", box.Items[0].Key(), box.Launched[0])
	}
}

func TestRemoteHostsNeverHoldTheBoardWhenItWaitsForNoOne(t *testing.T) {
	r := newRemoteHosts([]string{"box", "slow"}, 0, nil)
	r.ssh, r.timeout = fakeSSH(t), 2*time.Second
	start := time.Now()
	for _, b := range r.Boards() {
		if b.State() != "checking" {
			t.Errorf("%s: first read %q, want checking", b.Host, b.State())
		}
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("Boards waited %s", took)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if b := r.Boards(); b[0].State() == "1 sessions" && strings.HasPrefix(b[1].State(), "unreachable") {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("later reads = %+v", r.Boards())
}

func TestOneShotReadsCallAHostStillSilentAtTheirDeadlineUnreachable(t *testing.T) {
	r := newRemoteHosts([]string{"slow"}, time.Second, nil)
	r.ssh, r.timeout = fakeSSH(t), 5*time.Second
	if got := r.Boards()[0].State(); got != "unreachable: no answer in 1s" {
		t.Errorf("state = %q", got)
	}
}

func TestABoardCarriesLatelyClosedItemsWithTheirHistoryToAnotherHost(t *testing.T) {
	dir := t.TempDir()
	c, err := Wire(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Store.Close()
	open, err := c.Store.AddItem(core.Item{Session: "s1", Kind: core.KindDecision, Question: "still open?"})
	if err != nil {
		t.Fatal(err)
	}
	done, err := c.Store.AddItem(core.Item{Session: "s1", Kind: core.KindDecision, Question: "which word?"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Store.Transition(done.Key(), core.ItemAnswered, "D"); err != nil {
		t.Fatal(err)
	}
	closed, err := closedItems(c)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(boardJSON{Items: []itemJSON{withHistory(c, open)}, Closed: closed})
	if err != nil {
		t.Fatal(err)
	}
	var j boardJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		t.Fatal(err)
	}
	r := remoteBoard("box", j)
	if len(r.Closed) != 1 || r.Closed[0].Key() != "box/"+done.Key() || r.Closed[0].Answer != "D" {
		t.Fatalf("closed = %+v, want the answered item keyed by box", r.Closed)
	}
	if h := r.Closed[0].History; len(h) < 2 || h[len(h)-1].To != core.ItemAnswered || h[len(h)-1].Text != "D" {
		t.Errorf("history = %+v, want its answer", h)
	}
	if len(r.Items) != 1 || r.Items[0].Key() != "box/"+open.Key() {
		t.Errorf("open items = %+v", r.Items)
	}
}

func TestAHostReadsAsHowOldItsBoardIsAndHowLongItHasGoneWithoutAnswering(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join(dir, "board.json")
	script := filepath.Join(dir, "ssh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec cat "+board+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	withAt := func(at time.Time) string { return `{"at":"` + at.UTC().Format(time.RFC3339) + `",` + fakeBoard[1:] }
	for _, c := range []struct {
		name    string
		answers []string
		want    string
	}{
		{"fresh snapshot", []string{withAt(time.Now())}, "1 sessions"},
		{"snapshot from before its serve stalled", []string{withAt(time.Now().Add(-9 * time.Minute))}, "1 sessions, board 9m old: its ym serve is behind"},
		{"answered, then stopped answering", []string{withAt(time.Now().Add(-time.Minute)), ""}, "no answer for 0s (last good "},
		{"never answered", []string{""}, "unreachable: "},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := newRemoteHosts([]string{"box"}, 5*time.Second, nil)
			r.ssh, r.every = script, 0
			var got core.RemoteBoard
			for i, a := range c.answers {
				if err := os.WriteFile(board, []byte(a), 0o644); err != nil {
					t.Fatal(err)
				}
				got = r.Boards()[0]
				for deadline := time.Now().Add(3 * time.Second); i > 0 && got.Err == nil && time.Now().Before(deadline); {
					time.Sleep(20 * time.Millisecond)
					got = r.Boards()[0]
				}
			}
			if !strings.HasPrefix(got.State(), c.want) {
				t.Errorf("state %q, want it to start %q", got.State(), c.want)
			}
		})
	}
}

func TestBoardPrintsTheSnapshotYmServeTookAndBuildsNothing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	t.Setenv("YAMNAYA_NO_SERVE", "1")
	c, err := Wire(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Store.Close()
	run := func() (string, error) {
		var out strings.Builder
		root := NewRoot()
		root.SetArgs([]string{"board"})
		root.SetOut(&out)
		err := root.Execute()
		return out.String(), err
	}
	if _, err := run(); err == nil || !strings.Contains(err.Error(), "no board snapshot yet") {
		t.Fatalf("before any snapshot: %v, want it to say there is none yet", err)
	}
	if err := writeSnapshot(c, dir, core.Board{}); err != nil {
		t.Fatal(err)
	}
	out, err := run()
	var j boardJSON
	if err != nil || json.Unmarshal([]byte(out), &j) != nil || time.Since(parseTS(j.At)) > time.Minute {
		t.Fatalf("board printed %q, %v; want the snapshot stamped with when it was taken", out, err)
	}
}

func TestAHostThatStopsAnsweringIsSilentAndLosesItsSharedConnection(t *testing.T) {
	for _, c := range []struct {
		host   string
		silent bool
	}{{"box", false}, {"dead", true}, {"slow", true}} {
		t.Run(c.host, func(t *testing.T) {
			bin := fakeSSH(t)
			log := filepath.Join(t.TempDir(), "calls")
			wrapper := filepath.Join(t.TempDir(), "ssh")
			if err := os.WriteFile(wrapper, []byte("#!/bin/sh\necho \"$@\" >> "+log+"\nexec "+bin+" \"$@\"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			r := newRemoteHosts([]string{c.host}, 3*time.Second, nil)
			r.ssh, r.timeout = wrapper, time.Second
			r.Boards()
			calls, _ := os.ReadFile(log)
			dropped := strings.Contains(string(calls), "-O exit")
			if err := r.Silent(c.host); (err != nil) != c.silent || c.silent && !errors.Is(err, core.ErrUnreachable) {
				t.Errorf("Silent = %v", err)
			}
			if dropped != c.silent {
				t.Errorf("shared connection dropped %v; ssh calls:\n%s", dropped, calls)
			}
			if err := r.Reach(c.host); (err != nil) != (c.host != "box") {
				t.Errorf("Reach = %v", err)
			}
		})
	}
}

func TestBoardSeesHostsRememberedAndForgottenWhileItRuns(t *testing.T) {
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Setenv("YAMNAYA_SSH_BIN", fakeSSH(t))
	e := &env{core: &core.Core{Store: st}}
	if err := e.readRemotes(0, true); err != nil || e.core.Remotes == nil {
		t.Fatalf("board with no hosts wired no remotes: %v", err)
	}
	for _, c := range []struct {
		name string
		act  func() error
		want []string
	}{
		{"remembered", func() error { return st.RecordHost("box", time.Now()) }, []string{"box"}},
		{"forgotten", func() error { _, err := st.ForgetHost("box"); return err }, []string{}},
	} {
		if err := c.act(); err != nil {
			t.Fatal(err)
		}
		e.core.Remotes.(*remoteHosts).rereadAt = time.Time{}
		if got := e.core.Remotes.Hosts(); strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s: hosts %v, want %v", c.name, got, c.want)
		}
	}
}
