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
