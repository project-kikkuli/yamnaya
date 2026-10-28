package repair

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const state = "/Users/r/.local/state/yamnaya"

type fakeLocal struct {
	foreign int
	procs   []Proc
	states  map[int]string
	launchd []Launchd
	keepErr error
	dead    []string
	relays  []Relay
	locks   []string
	did     []string
}

func clean() *fakeLocal {
	return &fakeLocal{
		procs:   []Proc{{PID: 500, PPID: 1, UID: 501, Args: []string{"/Users/r/go/bin/ym", "serve", "--wait"}}},
		launchd: []Launchd{{Loaded: true, PID: 500, Plist: "io.yamnaya.ym-serve.plist", Program: "/Users/r/go/bin/ym", Want: "/Users/r/go/bin/ym"}},
	}
}

func (f *fakeLocal) Foreign() (int, string, string) {
	if f.foreign == 0 {
		return 0, "", ""
	}
	return f.foreign, state + "/ym.db", "sudo chown -R r:staff " + state + " /Users/r/.local/state/agent-slot"
}
func (f *fakeLocal) Procs() ([]Proc, error) { return f.procs, nil }
func (f *fakeLocal) UID() int               { return 501 }
func (f *fakeLocal) Self() int              { return 999 }
func (f *fakeLocal) ServeState(pid int) string {
	if s, ok := f.states[pid]; ok {
		return s
	}
	return state
}
func (f *fakeLocal) StateDir() string { return state }
func (f *fakeLocal) Socket() string   { return "ym" }
func (f *fakeLocal) Launchd() Launchd {
	l := f.launchd[0]
	if len(f.launchd) > 1 {
		f.launchd = f.launchd[1:]
	}
	return l
}
func (f *fakeLocal) Keep() (string, error) {
	f.did = append(f.did, "keep")
	return "io.yamnaya.ym-serve.plist", f.keepErr
}
func (f *fakeLocal) Kickstart() error {
	f.did = append(f.did, "kickstart")
	return nil
}
func (f *fakeLocal) Stop(pid int) error {
	f.did = append(f.did, "stop "+itoa(pid))
	return nil
}
func (f *fakeLocal) DeadSockets() []string { return f.dead }
func (f *fakeLocal) Remove(p string) error {
	f.did = append(f.did, "rm "+p)
	return nil
}
func (f *fakeLocal) Relays() ([]Relay, error) { return f.relays, nil }
func (f *fakeLocal) DropRelay(seq int64) error {
	f.did = append(f.did, "drop "+itoa(int(seq)))
	return nil
}
func (f *fakeLocal) StaleLocks() []string { return f.locks }

func itoa(n int) string { return strconv.Itoa(n) }

// rootYM is what a sudo ym left running: a root tmux server on ym's socket and a root codex under it.
var rootYM = []Proc{
	{PID: 4, PPID: 1, UID: 0, Args: []string{"tmux", "-L", "ym", "new-session", "-d"}},
	{PID: 5, PPID: 4, UID: 0, Args: []string{"/bin/zsh"}},
	{PID: 6, PPID: 5, UID: 0, Args: []string{"codex", "resume", "--yolo"}},
	{PID: 7, PPID: 1, UID: 0, Args: []string{"tmux", "-L", "other"}},
}

func TestLocalDoctorVerdicts(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(*fakeLocal)
		verdict string
		did     []string
	}{
		{"healthy", func(*fakeLocal) {}, "ym is healthy here: nothing runs as root, your state is yours, launchd keeps ym serve", nil},
		{"root-owned files", func(f *fakeLocal) { f.foreign = 3 },
			"run sudo chown -R r:staff " + state + " /Users/r/.local/state/agent-slot, then ym repair again: 3 files in ym's state are root-owned, so ym cannot update them", nil},
		{"root tmux and a root yolo codex", func(f *fakeLocal) { f.procs = append(f.procs, rootYM...) },
			"run sudo kill 4 5 6, then ym repair again: pid 6 runs codex as root with --yolo, unsandboxed with full rights over this machine, and root ym writes root-owned files into your state", nil},
		{"root processes before root files", func(f *fakeLocal) { f.procs, f.foreign = append(f.procs, rootYM[:2]...), 1 },
			"run sudo kill 4 5 && sudo chown -R r:staff " + state + " /Users/r/.local/state/agent-slot, then ym repair again: root ym processes write root-owned files into your state and their agents can change anything on this machine; 1 file in ym's state is root-owned, so ym cannot update them", nil},
		{"launchd not loaded", func(f *fakeLocal) {
			f.procs = nil
			f.launchd = []Launchd{{Plist: "p"}, {Loaded: true, Plist: "p"}}
		}, "ym works here again: launchd keeps ym serve again", []string{"keep", "kickstart"}},
		{"plist runs another binary", func(f *fakeLocal) {
			f.launchd = []Launchd{{Loaded: true, PID: 500, Plist: "p", Program: "/tmp/old/ym", Want: "/Users/r/go/bin/ym"}, {Loaded: true, PID: 500, Plist: "p"}}
		}, "ym works here again: launchd keeps ym serve again", []string{"keep"}},
		{"keep fails", func(f *fakeLocal) {
			f.launchd, f.keepErr = []Launchd{{Plist: "p"}}, errors.New("launchctl bootstrap: 5")
		},
			"launchd does not keep ym serve and ym service keep failed: launchctl bootstrap: 5", []string{"keep"}},
		{"stray serve outside launchd", func(f *fakeLocal) {
			f.procs = append(f.procs, Proc{PID: 8, PPID: 1, UID: 501, Args: []string{"ym", "serve"}},
				Proc{PID: 9, PPID: 1, UID: 501, Args: []string{"ym", "serve", "--once"}})
			f.states = map[int]string{9: "/tmp/drill/state"}
		}, "ym works here again: 1 stray ym serve stopped", []string{"stop 8", "kickstart"}},
		{"launchd checks skipped off macOS", func(f *fakeLocal) {
			f.launchd = []Launchd{{Why: "launchd is macOS only"}}
			f.procs = append(f.procs, Proc{PID: 8, PPID: 1, UID: 501, Args: []string{"ym", "serve"}})
		}, "ym is healthy here: nothing runs as root, your state is yours, launchd keeps ym serve", nil},
		{"dead sockets, stale relays and locks", func(f *fakeLocal) {
			f.dead, f.locks = []string{"/tmp/tmux-501/ymtest-1"}, []string{state + "/type-3.lock"}
			f.relays = []Relay{{Seq: 1, Dest: "gone", Forgotten: true}, {Seq: 2, Dest: "box", Refused: "no goal mgr9"}}
		}, "ym works here again: removed 1 dead socket, dropped 1 stale relay, removed 1 stale lock",
			[]string{"rm /tmp/tmux-501/ymtest-1", "drop 1", "rm " + state + "/type-3.lock"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := clean()
			c.set(f)
			var steps []Step
			got := Local(f, func(s Step) { steps = append(steps, s) })
			if got != c.verdict {
				t.Errorf("verdict\n got %q\nwant %q", got, c.verdict)
			}
			if !slices.Equal(f.did, c.did) {
				t.Errorf("did %v, want %v", f.did, c.did)
			}
			for _, s := range steps {
				if s.Status == Sudo && !strings.Contains(s.Detail, "root") && !strings.Contains(s.Detail, "sudo") {
					t.Errorf("sudo step %q names neither root nor the command", s.Detail)
				}
			}
		})
	}
}

func TestRootYMFindsRootServersAndWhatTheyStarted(t *testing.T) {
	procs := append(slices.Clone(rootYM),
		Proc{PID: 10, PPID: 1, UID: 0, Args: []string{"/usr/local/bin/ym", "serve"}},
		Proc{PID: 11, PPID: 10, UID: 0, Args: []string{"claude"}},
		Proc{PID: 12, PPID: 4, UID: 501, Args: []string{"claude"}},
		Proc{PID: 13, PPID: 1, UID: 0, Args: []string{"tmux", "-S", "/private/tmp/tmux-0/ym", "attach"}},
	)
	var got []int
	for _, p := range RootYM(procs, "ym") {
		got = append(got, p.PID)
	}
	if want := []int{4, 5, 6, 10, 11, 13}; !slices.Equal(got, want) {
		t.Fatalf("root ym %v, want %v", got, want)
	}
}

func TestParsePS(t *testing.T) {
	got := parsePS("  54465     1     0 tmux -L ym new-session\n  54985 54470     0 codex resume --yolo\nbad line\n")
	if len(got) != 2 || got[0].UID != 0 || got[1].PPID != 54470 || got[1].Args[2] != "--yolo" {
		t.Fatalf("parsePS = %+v", got)
	}
}
