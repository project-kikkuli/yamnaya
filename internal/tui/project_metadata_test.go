package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type metadataRemotes struct {
	remotes
	store *core.Store
	err   error
}

func (r *metadataRemotes) Run(host string, args ...string) (string, error) {
	r.ran = append(r.ran, append([]string{host}, args...))
	if r.err != nil {
		return "", r.err
	}
	switch args[0] + " " + args[1] {
	case "project rename":
		return "renamed", r.store.Rename(args[2], args[3])
	case "project merge":
		return "merged", r.store.Merge(args[2], args[3])
	case "project split":
		_, err := r.store.Split(args[2])
		return "split", err
	case "phase set":
		if len(args) != 5 || args[3] != "-p" {
			return "", fmt.Errorf("missing project target: %v", args)
		}
		_, err := r.store.SetPhases(args[4], strings.Split(args[2], ","))
		return "phases set", err
	case "phase next":
		if len(args) != 4 || args[2] != "-p" {
			return "", fmt.Errorf("missing project target: %v", args)
		}
		_, err := r.store.NextPhase(args[3])
		return "phase advanced", err
	}
	if args[0] == "vibe" && len(args) == 6 && args[2] == "--why" && args[4] == "-p" {
		_, err := r.store.AddVibe(core.Vibe{Place: args[1], Why: args[3], Target: args[5]})
		return "vibe set", err
	}
	return "", fmt.Errorf("unexpected remote command: %v", args)
}

func metadataState(t *testing.T, st *core.Store) []any {
	t.Helper()
	lin, err := st.Lineage()
	if err != nil {
		t.Fatal(err)
	}
	phases, err := st.AllPhases()
	if err != nil {
		t.Fatal(err)
	}
	vibes, err := st.Vibes()
	if err != nil {
		t.Fatal(err)
	}
	return []any{lin, phases, vibes}
}

func metadataFixture(t *testing.T) (Model, *core.Core, *metadataRemotes) {
	t.Helper()
	m, c, _ := setup(t)
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "remote.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, store := range []*core.Store{c.Store, st} {
		if err := store.Rename("twin", "Baseline"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SetPhases("twin", []string{"A", "B"}); err != nil {
			t.Fatal(err)
		}
		if err := store.Merge("child", "twin"); err != nil {
			t.Fatal(err)
		}
	}
	r := &metadataRemotes{store: st, remotes: remotes{boards: []core.RemoteBoard{{Host: "box", At: time.Now(),
		Missions: map[string]core.Stats{"twin": {Phases: core.Phases{Names: []string{"A", "B"}}}},
		Sessions: []core.Live{
			{Session: core.Session{ID: "twin", Harness: "claude", Title: "Remote owner"}, Project: "twin"},
			{Session: core.Session{ID: "lead", Harness: "claude", Title: "Remote destination"}, Project: "lead"},
		},
	}}}}
	c.Remotes, c.ShowRemotes = r, true
	return refresh(t, m), c, r
}

func projectOnHost(key, host string) func(row) bool {
	return func(r row) bool {
		return r.project != nil && r.session == nil && r.project.Key == key && core.ProjectHost(*r.project) == host
	}
}

func TestMissionMetadataWritesOnlyToSelectedHost(t *testing.T) {
	for _, action := range []struct {
		name, key, text string
	}{
		{"rename", "e", "Changed"}, {"split", "S", ""}, {"next phase", "p", ""},
		{"set phases", "P", "X,Y"}, {"vibe", "v", "Kyoto: progressing"},
	} {
		for _, target := range []string{"local", "remote", "remote failure", "remote removed during prompt"} {
			if target == "remote removed during prompt" && action.text == "" {
				continue
			}
			t.Run(action.name+"/"+target, func(t *testing.T) {
				m, c, r := metadataFixture(t)
				host := ""
				if target != "local" {
					host = "box"
					g, err := c.Store.AddGoal(core.Goal{Ask: "verify local work", Repo: t.TempDir()})
					if err != nil {
						t.Fatal(err)
					}
					if _, err := c.Store.AddVerification(core.Verification{Goal: g.ID, Session: "twin"}); err != nil {
						t.Fatal(err)
					}
					m = refresh(t, m)
				}
				if target == "remote failure" {
					r.err = errors.New("remote write refused")
				}
				beforeLocal, beforeRemote := metadataState(t, c.Store), metadataState(t, r.store)
				m = to(t, m, projectOnHost("twin", host))
				m = press(t, m, action.key)
				if target == "remote removed during prompt" {
					c.Remotes = nil
				}
				if action.text != "" {
					m = press(t, m, "ctrl+u", "paste:"+action.text, "enter")
				} else if action.key == "p" {
					m = press(t, m, "y")
				}
				local, remote := metadataState(t, c.Store), metadataState(t, r.store)
				if target == "remote failure" || target == "remote removed during prompt" {
					if !reflect.DeepEqual(local, beforeLocal) || !reflect.DeepEqual(remote, beforeRemote) || !strings.Contains(m.message, "error:") {
						t.Fatalf("remote failure changed metadata: local=%v remote=%v message=%q", local, remote, m.message)
					}
					return
				}
				changed := c.Store
				if host == "" {
					if len(r.ran) != 0 || !reflect.DeepEqual(remote, beforeRemote) {
						t.Fatalf("local action touched remote: calls=%v state=%v", r.ran, remote)
					}
				} else {
					changed = r.store
					if len(r.ran) != 1 || r.ran[0][0] != "box" || !reflect.DeepEqual(local, beforeLocal) {
						t.Fatalf("remote action touched local: calls=%v state=%v", r.ran, local)
					}
				}
				lin, _ := changed.Lineage()
				ph, _ := changed.Phases("twin")
				vs, _ := changed.Vibes()
				ok := false
				switch action.name {
				case "rename":
					ok = lin.Name["twin"] == "Changed"
				case "split":
					ok = lin.Merged["child"] == ""
				case "next phase":
					ok = ph.Current == 1
				case "set phases":
					ok = reflect.DeepEqual(ph.Names, []string{"X", "Y"})
				case "vibe":
					ok = len(vs) == 1 && vs[0].Place == "Kyoto" && vs[0].Target == "twin" && vs[0].Why == "progressing"
				}
				if !ok {
					t.Fatalf("selected metadata did not change: lineage=%+v phases=%+v vibes=%v message=%q", lin, ph, vs, m.message)
				}
			})
		}
	}
}

func TestMissionMergeRequiresTheSameHost(t *testing.T) {
	for _, hosts := range [][2]string{{"", ""}, {"box", "box"}, {"", "box"}, {"box", ""}} {
		t.Run(hosts[0]+" to "+hosts[1], func(t *testing.T) {
			m, c, r := metadataFixture(t)
			localBefore, remoteBefore := metadataState(t, c.Store), metadataState(t, r.store)
			m = to(t, m, projectOnHost("twin", hosts[0]))
			m = press(t, m, "m")
			for range m.cursor {
				m = press(t, m, "up")
			}
			m = to(t, m, projectOnHost("lead", hosts[1]))
			m = press(t, m, "enter")
			local, remote := metadataState(t, c.Store), metadataState(t, r.store)
			if hosts[0] != hosts[1] {
				if len(r.ran) != 0 || !reflect.DeepEqual(local, localBefore) || !reflect.DeepEqual(remote, remoteBefore) || !strings.Contains(m.message, "different hosts") {
					t.Fatalf("cross-host merge mutated a store: calls=%v local=%v remote=%v message=%q", r.ran, local, remote, m.message)
				}
				return
			}
			changed := c.Store
			if hosts[0] == "box" {
				changed = r.store
				if len(r.ran) != 1 || !reflect.DeepEqual(local, localBefore) {
					t.Fatalf("remote merge touched local: calls=%v state=%v", r.ran, local)
				}
			} else if len(r.ran) != 0 || !reflect.DeepEqual(remote, remoteBefore) {
				t.Fatalf("local merge touched remote: calls=%v state=%v", r.ran, remote)
			}
			lin, _ := changed.Lineage()
			if lin.Merged["twin"] != "lead" {
				t.Fatalf("same-host merge missing: %+v, message=%q", lin, m.message)
			}
		})
	}
}
