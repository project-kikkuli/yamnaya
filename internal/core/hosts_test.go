package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHostsAreRememberedOnFirstUseAndForgotten(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := time.Unix(1000, 0)
	for i, h := range []string{"box", "user@other", "box"} {
		if err := st.RecordHost(h, t0.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SeeHost("never-used", t0); err != nil {
		t.Fatal(err)
	}
	hosts, err := st.KnownHosts()
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 2 || hosts[0].Name != "box" || !hosts[0].FirstUsed.Equal(t0) || !hosts[0].LastSeen.Equal(t0.Add(2*time.Minute)) || hosts[1].Name != "user@other" {
		t.Fatalf("hosts = %+v", hosts)
	}
	for _, tc := range []struct {
		host string
		want bool
	}{{"box", true}, {"box", false}, {"never-used", false}} {
		if got, err := st.ForgetHost(tc.host); err != nil || got != tc.want {
			t.Errorf("forget %s = %v, %v; want %v", tc.host, got, err, tc.want)
		}
	}
}

func TestTheLaunchTargetIsThisHostUntilOneIsChosen(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got, err := st.LaunchTarget(); err != nil || got != "" {
		t.Fatalf("fresh launch target = %q, %v; want this host", got, err)
	}
	for _, want := range []string{"box", "other", ""} {
		if err := st.SetLaunchTarget(want); err != nil {
			t.Fatal(err)
		}
		if got, err := st.LaunchTarget(); err != nil || got != want {
			t.Fatalf("launch target = %q, %v; want %q", got, err, want)
		}
	}
}

func TestAKeptBoardIsReplacedOnlyOnceItIsAMinuteOld(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t0 := time.Unix(1000, 0)
	for _, tc := range []struct {
		after time.Duration
		board string
		want  string
		at    time.Duration
	}{
		{0, "first", "first", 0},
		{5 * time.Second, "five seconds on", "first", 0},
		{59 * time.Second, "a second short", "first", 0},
		{time.Minute, "a minute on", "a minute on", time.Minute},
	} {
		if err := st.KeepBoard("box", t0.Add(tc.after), []byte(tc.board)); err != nil {
			t.Fatal(err)
		}
		at, board, ok, err := st.KeptBoard("box")
		if err != nil || !ok || string(board) != tc.want || !at.Equal(t0.Add(tc.at)) {
			t.Errorf("after %v kept %q at %v (%v, %v); want %q at %v", tc.after, board, at, ok, err, tc.want, t0.Add(tc.at))
		}
	}
}
