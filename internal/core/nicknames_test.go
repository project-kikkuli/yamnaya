package core

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

const box = "dev-team-boxmachine.example.net"

func TestNicknamesAreTheSetOnesElseTheShortestTailNoOtherHostShares(t *testing.T) {
	for _, c := range []struct {
		name  string
		hosts []string
		set   map[string]string
		want  map[string]string
	}{
		{"one host", []string{box}, nil, map[string]string{box: "boxmachine"}},
		{"shared tail grows", []string{"a-prod-box.x", "b-prod-box.y"}, nil, map[string]string{"a-prod-box.x": "a-prod-box", "b-prod-box.y": "b-prod-box"}},
		{"distinct tails", []string{"stg-runner.x", "prod-builder.x"}, nil, map[string]string{"stg-runner.x": "runner", "prod-builder.x": "builder"}},
		{"user and plain name", []string{"me@laptop"}, nil, map[string]string{"me@laptop": "laptop"}},
		{"address", []string{"10.0.0.7"}, nil, map[string]string{"10.0.0.7": "10.0.0.7"}},
		{"same label everywhere", []string{"box.a", "box.b"}, nil, map[string]string{"box.a": "box.a", "box.b": "box.b"}},
		{"set nickname wins", []string{box}, map[string]string{box: "far"}, map[string]string{box: "far"}},
		{"a default another host was renamed to falls back to the full name", []string{"stg-runner.x", "prod-builder.x"},
			map[string]string{"prod-builder.x": "runner"}, map[string]string{"stg-runner.x": "stg-runner.x", "prod-builder.x": "runner"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Nicknames(c.hosts, c.set); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("Nicknames(%v, %v) = %v, want %v", c.hosts, c.set, got, c.want)
			}
		})
	}
}

func TestRenameHostKeepsNicknamesUnique(t *testing.T) {
	other := "stg-runner.example.net"
	for _, c := range []struct {
		name, host, nick string
		want             string
		err              string
	}{
		{"new nickname", box, "far", "far", ""},
		{"leading @ is dropped", box, "@far", "far", ""},
		{"empty restores the default", box, "", "boxmachine", ""},
		{"another host's default", box, "runner", "", "@runner already names " + other},
		{"another host's set nickname", box, "near", "", "@near already names " + other},
		{"another host's full name", box, other, "", "already names " + other},
		{"its own default again", box, "boxmachine", "boxmachine", ""},
		{"spaces", box, "far box", "", "no spaces"},
		{"slash", box, "far/x", "", "no spaces"},
		{"unknown host", "nowhere.example.net", "far", "", "does not remember"},
	} {
		t.Run(c.name, func(t *testing.T) {
			st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			for _, h := range []string{box, other} {
				if err := st.RecordHost(h, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(c.name, "set nickname") {
				if _, err := st.RenameHost(other, "near"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := st.RenameHost(box, "old"); err != nil {
				t.Fatal(err)
			}
			got, err := st.RenameHost(c.host, c.nick)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("RenameHost(%s, %q) = %q, %v; want an error saying %q", c.host, c.nick, got, err, c.err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("RenameHost(%s, %q) = %q, %v; want %q", c.host, c.nick, got, err, c.want)
			}
			nicks, err := st.HostNicknames()
			if err != nil || nicks[box] != c.want {
				t.Fatalf("after renaming, %s is @%s (%v), want @%s", box, nicks[box], err, c.want)
			}
		})
	}
}

func TestResolveHostTakesTheFullNameOrTheNickname(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.RecordHost(box, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameHost(box, "far"); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{box, "far", "@far"} {
		if h, err := st.ResolveHost(ref); err != nil || h != box {
			t.Errorf("ResolveHost(%q) = %q, %v; want %s", ref, h, err, box)
		}
	}
	if _, err := st.ResolveHost("@boxmachine"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the default nickname a host was renamed away from still resolves: %v", err)
	}
	if _, err := st.ForgetHost(box); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordHost(box, time.Now()); err != nil {
		t.Fatal(err)
	}
	if nicks, _ := st.HostNicknames(); nicks[box] != "boxmachine" {
		t.Errorf("a forgotten host comes back as @%s, want its default @boxmachine", nicks[box])
	}
}
