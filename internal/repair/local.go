package repair

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Proc is one process with the user it runs as.
type Proc struct {
	PID, PPID, UID int
	Args           []string
}

func (p Proc) String() string {
	return strconv.Itoa(p.PID) + " " + strings.Join(p.Args, " ")
}

// Launchd is how launchd keeps this state directory's ym serve: Why says why it is not checked here, when it is not.
type Launchd struct {
	Why           string
	Loaded        bool
	PID           int
	Plist         string
	Program, Want string
}

// Relay is a message this host still holds for another one.
type Relay struct {
	Seq       int64
	Dest      string
	Refused   string
	Forgotten bool
}

// LocalProbes are what the local doctor looks at and fixes on this machine.
type LocalProbes interface {
	// Foreign counts the files in ym's state another user owns, names the first, and gives the command that returns them.
	Foreign() (int, string, string)
	Procs() ([]Proc, error)
	UID() int
	Self() int
	// ServeState is the state directory the ym serve with pid works on.
	ServeState(pid int) string
	StateDir() string
	// Socket is ym's tmux server's socket name.
	Socket() string
	Launchd() Launchd
	Keep() (string, error)
	Kickstart() error
	Stop(pid int) error
	DeadSockets() []string
	Remove(path string) error
	Relays() ([]Relay, error)
	DropRelay(seq int64) error
	StaleLocks() []string
}

// ReadProcs lists every process with its user, as ps sees them.
func ReadProcs() ([]Proc, error) {
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,uid=,args=").Output()
	if err != nil {
		return nil, err
	}
	return parsePS(string(out)), nil
}

func parsePS(out string) []Proc {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		uid, e3 := strconv.Atoi(f[2])
		if e1 != nil || e2 != nil || e3 != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, UID: uid, Args: f[3:]})
	}
	return procs
}

// RootYM are the processes root runs as ym: a root ym, a root tmux server on ym's socket, and everything either started.
func RootYM(procs []Proc, socket string) []Proc {
	roots := map[int]bool{}
	for _, p := range procs {
		if p.UID == 0 && (isYM(p) || isYMTmux(p, socket)) {
			roots[p.PID] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range procs {
			if p.UID == 0 && !roots[p.PID] && roots[p.PPID] {
				roots[p.PID], changed = true, true
			}
		}
	}
	var out []Proc
	for _, p := range procs {
		if roots[p.PID] {
			out = append(out, p)
		}
	}
	return out
}

func isYM(p Proc) bool { return len(p.Args) > 0 && filepath.Base(p.Args[0]) == "ym" }

func isYMTmux(p Proc, socket string) bool {
	if len(p.Args) == 0 || !strings.HasPrefix(filepath.Base(p.Args[0]), "tmux") {
		return false
	}
	for i := 1; i+1 < len(p.Args); i++ {
		if p.Args[i] == "-L" && p.Args[i+1] == socket || p.Args[i] == "-S" && filepath.Base(p.Args[i+1]) == socket {
			return true
		}
	}
	return false
}

// isServe is a ym serve, however it was started.
func isServe(p Proc) bool { return isYM(p) && len(p.Args) > 1 && p.Args[1] == "serve" }

type localDoc struct {
	p     LocalProbes
	emit  func(Step)
	sudo  []string
	why   []string
	fails []string
	fixed []string
}

// Local finds and fixes what keeps ym from working on this machine, emitting each check as it ends, and returns one line saying
// what is left to do. What needs sudo it only shows.
func Local(p LocalProbes, emit func(Step)) string {
	d := &localDoc{p: p, emit: emit}
	d.rootProcs()
	d.files()
	d.launchd()
	d.sockets()
	d.relays()
	d.locks()
	return d.verdict()
}

func (d *localDoc) step(name string, st Status, format string, args ...any) {
	d.emit(Step{Name: name, Status: st, Detail: fmt.Sprintf(format, args...)})
}

func (d *localDoc) rootProcs() {
	procs, err := d.p.Procs()
	if err != nil {
		d.step("procs", Failed, "ps: %v", err)
		d.fails = append(d.fails, "ps failed: "+err.Error())
		return
	}
	if d.p.UID() == 0 {
		d.step("procs", Skipped, "running as root, so root processes are ym's own; run ym repair as yourself")
		return
	}
	root := RootYM(procs, d.p.Socket())
	if len(root) == 0 {
		d.step("procs", OK, "no ym process runs as root")
		return
	}
	var pids []string
	for _, p := range root {
		pids = append(pids, strconv.Itoa(p.PID))
		d.step("procs", Sudo, "root: %s", trimArgs(p))
	}
	why := "root ym processes write root-owned files into your state and their agents can change anything on this machine"
	for _, p := range root {
		if slices.ContainsFunc(p.Args, func(a string) bool { return a == "--yolo" || strings.HasPrefix(a, "--dangerously") }) {
			why = fmt.Sprintf("pid %d runs %s as root with %s, unsandboxed with full rights over this machine, and root ym writes root-owned files into your state", p.PID, filepath.Base(p.Args[0]), yoloFlag(p))
			break
		}
	}
	cmd := "sudo kill " + strings.Join(pids, " ")
	d.step("procs", Sudo, "run: %s (%s)", cmd, why)
	d.sudo = append(d.sudo, cmd)
	d.why = append(d.why, why)
}

func (d *localDoc) files() {
	n, first, fix := d.p.Foreign()
	if n == 0 {
		d.step("files", OK, "ym's state and agent-slot belong to you")
		return
	}
	d.step("files", Sudo, "%d entries belong to another user, first %s; run: %s", n, first, fix)
	d.sudo = append(d.sudo, fix)
	d.why = append(d.why, isAre(plural(n, "file")+" in ym's state", n)+" root-owned, so ym cannot update them")
}

func (d *localDoc) launchd() {
	l := d.p.Launchd()
	if l.Why != "" {
		d.step("launchd", Skipped, "%s", l.Why)
		return
	}
	switch {
	case !l.Loaded:
		d.step("launchd", Failed, "not loaded")
	case l.Program != l.Want:
		d.step("launchd", Failed, "%s runs %s, not %s", l.Plist, l.Program, l.Want)
	default:
		d.step("launchd", OK, "%s keeps ym serve (pid %d)", l.Plist, l.PID)
	}
	if !l.Loaded || l.Program != l.Want {
		path, err := d.p.Keep()
		if err != nil {
			d.step("launchd", Failed, "ym service keep: %v", err)
			d.fails = append(d.fails, "launchd does not keep ym serve and ym service keep failed: "+err.Error())
			return
		}
		d.step("launchd", Fixed, "rewrote and loaded %s", path)
		d.fixed = append(d.fixed, "launchd keeps ym serve again")
		l = d.p.Launchd()
	}
	procs, err := d.p.Procs()
	if err != nil {
		return
	}
	state, uid, self := d.p.StateDir(), d.p.UID(), d.p.Self()
	stopped := 0
	for _, p := range procs {
		if p.UID != uid || p.PID == self || p.PID == l.PID || !isServe(p) || d.p.ServeState(p.PID) != state {
			continue
		}
		if err := d.p.Stop(p.PID); err != nil {
			d.step("serve", Failed, "stop stray %s: %v", trimArgs(p), err)
			d.fails = append(d.fails, fmt.Sprintf("a ym serve outside launchd (pid %d) would not stop: %v; kill %d", p.PID, err, p.PID))
			continue
		}
		d.step("serve", Fixed, "stopped stray %s, which launchd did not start", trimArgs(p))
		stopped++
	}
	if stopped > 0 {
		d.fixed = append(d.fixed, plural(stopped, "stray ym serve")+" stopped")
	}
	if l = d.p.Launchd(); l.Loaded && (l.PID == 0 || stopped > 0) {
		if err := d.p.Kickstart(); err != nil {
			d.step("serve", Failed, "launchctl kickstart: %v", err)
			d.fails = append(d.fails, "launchd's ym serve would not start: "+err.Error())
			return
		}
		d.step("serve", Fixed, "launchd's ym serve started")
	} else if stopped == 0 {
		d.step("serve", OK, "only launchd's ym serve runs")
	}
}

func (d *localDoc) sockets() {
	dead := d.p.DeadSockets()
	removed := 0
	for _, s := range dead {
		if err := d.p.Remove(s); err != nil {
			d.step("sockets", Failed, "remove %s: %v", s, err)
			continue
		}
		removed++
	}
	if removed == 0 && len(dead) == 0 {
		d.step("sockets", OK, "no dead tmux or ssh sockets")
		return
	}
	if removed > 0 {
		d.step("sockets", Fixed, "removed %s nothing listens on", plural(removed, "dead socket"))
		d.fixed = append(d.fixed, "removed "+plural(removed, "dead socket"))
	}
}

func (d *localDoc) relays() {
	rs, err := d.p.Relays()
	if err != nil {
		d.step("relays", Failed, "%v", err)
		return
	}
	dropped := 0
	for _, r := range rs {
		switch {
		case r.Forgotten:
			if err := d.p.DropRelay(r.Seq); err != nil {
				d.step("relays", Failed, "drop relay %d to forgotten %s: %v", r.Seq, r.Dest, err)
				continue
			}
			dropped++
		case r.Refused != "":
			d.step("relays", Skipped, "relay %d to %s is refused there (%s); ym relay drop %d stops it", r.Seq, r.Dest, r.Refused, r.Seq)
		}
	}
	if dropped > 0 {
		d.step("relays", Fixed, "dropped %s to hosts ym no longer remembers", plural(dropped, "relay"))
		d.fixed = append(d.fixed, "dropped "+plural(dropped, "stale relay"))
	} else {
		d.step("relays", OK, "%s waiting, none stale", plural(len(rs), "relay"))
	}
}

func (d *localDoc) locks() {
	stale := d.p.StaleLocks()
	removed := 0
	for _, l := range stale {
		if d.p.Remove(l) == nil {
			removed++
		}
	}
	if removed == 0 {
		d.step("locks", OK, "no stale locks")
		return
	}
	d.step("locks", Fixed, "removed %s for panes that are gone", plural(removed, "unheld lock"))
	d.fixed = append(d.fixed, "removed "+plural(removed, "stale lock"))
}

func (d *localDoc) verdict() string {
	switch {
	case len(d.sudo) > 0:
		return fmt.Sprintf("run %s, then ym repair again: %s", strings.Join(d.sudo, " && "), strings.Join(d.why, "; "))
	case len(d.fails) > 0:
		return d.fails[0]
	case len(d.fixed) > 0:
		return "ym works here again: " + strings.Join(d.fixed, ", ")
	}
	return "ym is healthy here: nothing runs as root, your state is yours, launchd keeps ym serve"
}

func trimArgs(p Proc) string {
	s := p.String()
	if len(s) > 120 {
		s = s[:119] + "…"
	}
	return s
}

func yoloFlag(p Proc) string {
	for _, a := range p.Args {
		if a == "--yolo" || strings.HasPrefix(a, "--dangerously") {
			return a
		}
	}
	return ""
}

func plural(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return strconv.Itoa(n) + " " + what + "s"
}
