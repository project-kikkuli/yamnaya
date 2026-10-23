package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// idleAgent is agent sitting idle in /src/w.
type idleAgent struct{ agent }

func (a idleAgent) Resume(id string) []string { return []string{"claude", "--resume", id} }
func (a idleAgent) Discover(procs []proc.Proc) []core.Session {
	out := a.agent.Discover(procs)
	for i := range out {
		out[i].Status, out[i].Cwd = core.Idle, "/src/w"
	}
	return out
}

// relauncher records relaunches; ym's other launcher actions are unused here.
type relauncher struct {
	core.Launches
	got []string
	err error
}

func (r *relauncher) Relaunch(from core.Live, harness, id string) (string, error) {
	r.got = append(r.got, core.Short(from.Key())+" "+harness+" "+id+" "+from.Cwd)
	return "revived adopt-" + id, r.err
}

func (r *relauncher) Dormant() []core.Dormant   { return nil }
func (r *relauncher) Launched() []core.Launched { return nil }

func TestSwitchRelaunchesOnlyWithTheFlag(t *testing.T) {
	for _, c := range []struct {
		name      string
		relaunch  bool
		stubborn  bool
		portErr   error
		launchErr error
		signal    bool
		ported    bool
		launched  bool
		want      string
	}{
		{"no flag ports and prints the resume command", false, false, nil, nil, false, true, false, "ported to codex session p-1"},
		{"--relaunch ends, ports and relaunches", true, false, nil, nil, true, true, true, "relaunched 06456f12 as codex p-1: revived adopt-p-1"},
		{"--relaunch, still running", true, true, nil, nil, true, false, false, "still running (pid 4242) after SIGTERM; not relaunched; if it ended, resume it with: cd /src/w && claude --resume 06456f12-aaaa"},
		{"--relaunch, port fails", true, false, errors.New("cs broke"), nil, true, true, false, "porting failed: cs broke; resume it with: cd /src/w && claude --resume 06456f12-aaaa"},
		{"--relaunch, launch fails", true, false, nil, errors.New("no tmux"), true, true, true, "starting it failed: no tmux; resume it with: cd /src/w && "},
	} {
		t.Run(c.name, func(t *testing.T) {
			dead := false
			var sent []int
			var ported []string
			st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			l := &relauncher{err: c.launchErr}
			k := &core.Core{Store: st, Harnesses: []core.Harness{idleAgent{}}, Launches: l, KillGrace: 1,
				Procs: func() ([]proc.Proc, error) {
					if dead {
						return nil, nil
					}
					return []proc.Proc{{PID: 4242, TTY: "/dev/ttys999", Args: []string{"claude"}}}, nil
				},
				Signal: func(pid int, sig syscall.Signal) error {
					sent, dead = append(sent, pid), !c.stubborn
					return nil
				},
				Port: func(cwd, id, to string) (string, error) {
					ported = append(ported, cwd+" "+id+" "+to)
					return "p-1", c.portErr
				}}
			var out bytes.Buffer
			err = switchSession(k, "06456f12", "codex", c.relaunch, &out)
			got := out.String()
			if err != nil {
				got += err.Error()
			}
			wantPort := []string(nil)
			if c.ported {
				wantPort = []string{"/src/w 06456f12-aaaa codex"}
			}
			wantLaunch := []string(nil)
			if c.launched {
				wantLaunch = []string{"06456f12 codex p-1 /src/w"}
			}
			if !strings.Contains(got, c.want) || (len(sent) == 1) != c.signal || !slices.Equal(ported, wantPort) || !slices.Equal(l.got, wantLaunch) {
				t.Fatalf("got %q, signalled %v, ported %v, relaunched %v; want %q", got, sent, ported, l.got, c.want)
			}
		})
	}
}

func TestRelaunchTakesOverTheLaunchRecordInTheSessionsDirectory(t *testing.T) {
	cwd, elsewhere := t.TempDir(), t.TempDir()
	launched := launch.Record{Key: "app-wt-x", Harness: "claude", Session: "06456f12-aaaa", Prompt: "fix it", Worktree: launch.Worktree{Path: cwd, Branch: "x", GitDir: "/src/app/.git"}}
	for _, c := range []struct {
		name  string
		saved []launch.Record
		want  []launch.Record
	}{
		{"launched here: same key and worktree, now the port", []launch.Record{launched},
			[]launch.Record{{Key: "app-wt-x", Harness: "codex", Session: "p-1", Prompt: "fix it", Worktree: launched.Worktree}}},
		{"never launched: adopted under the port's key", nil,
			[]launch.Record{{Key: "adopt-p-1", Harness: "codex", Session: "p-1", Prompt: "Lead work", Worktree: launch.Worktree{Path: cwd}, Adopted: true}}},
		{"launched elsewhere: that record stays, this directory is adopted", []launch.Record{{Key: "old", Harness: "claude", Session: "06456f12-aaaa", Worktree: launch.Worktree{Path: elsewhere}}},
			[]launch.Record{{Key: "adopt-p-1", Harness: "codex", Session: "p-1", Prompt: "Lead work", Worktree: launch.Worktree{Path: cwd}, Adopted: true}, {Key: "old", Harness: "claude", Session: "06456f12-aaaa", Worktree: launch.Worktree{Path: elsewhere}}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := launcher{records: launch.Records{Dir: filepath.Join(t.TempDir(), "launches")}}
			for _, r := range c.saved {
				if err := l.records.Save(r); err != nil {
					t.Fatal(err)
				}
			}
			from := core.Live{Session: core.Session{Harness: "claude", ID: "06456f12-aaaa", Cwd: cwd, Title: "Lead work"}}
			rec, err := l.takeOver(from, "codex", "p-1")
			if err != nil {
				t.Fatal(err)
			}
			all, _ := l.records.All()
			got := map[string]launch.Record{}
			for _, r := range all {
				r.Started, r.Head = time.Time{}, ""
				got[r.Key] = r
			}
			if len(got) != len(c.want) || rec.Key != c.want[0].Key {
				t.Fatalf("took over %s; records %+v, want %+v", rec.Key, got, c.want)
			}
			for _, w := range c.want {
				if w.Harness == "codex" {
					w.Profile = "default"
					root, err := filepath.EvalSymlinks(os.Getenv("CODEX_HOME"))
					if err != nil {
						t.Fatal(err)
					}
					w.ConfigRoot = root
					w.SQLiteRoot = root
					w.ConfigExplicit = true
				}
				if g, ok := got[w.Key]; !ok || !reflect.DeepEqual(g, w) {
					t.Fatalf("record %s = %+v, want %+v", w.Key, g, w)
				}
			}
		})
	}
}
