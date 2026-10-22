package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
