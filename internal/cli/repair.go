package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/repair"
)

// sourceVersion names the source this ym was built from, the same for every build of it on any host; go.mod and go.sum are
// left out, since ym remote install edits them to build.
var sourceVersion = sync.OnceValue(func() string {
	h := sha256.New()
	fs.WalkDir(yamnaya.Source, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path == "go.mod" || path == "go.sum" {
			return nil
		}
		raw, _ := yamnaya.Source.ReadFile(path)
		fmt.Fprintf(h, "%s\x00%d\x00", path, len(raw))
		h.Write(raw)
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:12]
})

func versionCmd() *cobra.Command {
	return &cobra.Command{Use: "version", Hidden: true, Short: "Print the version of ym's source, which ym remote repair compares across hosts", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintln(cmd.OutOrStdout(), sourceVersion())
			return nil
		}}
}

func remoteRepairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repair <host|@nickname>",
		Short: "Check each step from here to a host, fix what ym safely can, and say in one line what is wrong and what to do",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, name := repairTarget(args[0])
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "repairing the way to %s (%s)\n", name, host)
			verdict := repair.Host(host, name, newHostProbes(), func(s repair.Step) { fmt.Fprintln(out, s) })
			fmt.Fprintln(out, repair.VerdictPrefix+verdict)
			return nil
		},
	}
}

// repairTarget is the host ref names, by its full name or nickname, and the name the board shows for it.
func repairTarget(ref string) (string, string) {
	path, err := core.DefaultStorePath()
	if err != nil {
		return ref, ref
	}
	st, err := core.OpenStore(path)
	if err != nil {
		return strings.TrimPrefix(ref, "@"), ref
	}
	defer st.Close()
	host, err := st.ResolveHost(ref)
	if err != nil {
		return strings.TrimPrefix(ref, "@"), ref
	}
	if nicks, err := st.HostNicknames(); err == nil && nicks[host] != "" {
		return host, "@" + nicks[host]
	}
	return host, host
}

func repairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "repair",
		Short: "Find and fix what keeps ym from working on this host; what needs sudo is shown, never run",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "repairing ym on %s\n", core.ThisHost())
			verdict := repair.Local(localProbes{}, func(s repair.Step) { fmt.Fprintln(out, s) })
			fmt.Fprintln(out, repair.VerdictPrefix+verdict)
			return nil
		},
	}
}

type hostProbes struct {
	repair.Net
	ssh string
	r   *remoteHosts
}

func newHostProbes() hostProbes {
	r := newRemoteHosts(nil, 0, nil)
	return hostProbes{Net: repair.NewNet(), ssh: r.ssh, r: r}
}

// ssh runs ssh with args within d, returning what it printed and its last stderr line on failure.
func (p hostProbes) run(d time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	c := exec.CommandContext(ctx, p.ssh, args...)
	c.WaitDelay = time.Second
	var errb bytes.Buffer
	c.Stderr = &errb
	out, err := c.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("no answer in %s", d)
	}
	if err != nil {
		return "", sshErr(err, errb.Bytes())
	}
	return string(out), nil
}

func (p hostProbes) Auth(host string) error {
	_, err := p.run(45*time.Second, "-o", "BatchMode=yes", "-o", "ConnectTimeout=35", "-o", "ControlPath=none", host, "true")
	return err
}

// socket is the path of ym's shared connection to host, as ssh expands it.
func (p hostProbes) socket(host string) string {
	out, err := p.run(5*time.Second, append([]string{"-G"}, sshArgs(host)...)...)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(line, "controlpath "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (p hostProbes) Master(host string) repair.Master {
	path := p.socket(host)
	if _, err := os.Stat(path); path == "" || err != nil {
		return repair.Master{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, p.ssh, sshArgs(host, "-O", "check")...).CombinedOutput()
	detail := strings.TrimSpace(string(out))
	return repair.Master{Socket: true, Alive: err == nil, Detail: detail}
}

func (p hostProbes) DropMaster(host string) { p.r.drop(host) }

func (p hostProbes) ClearSocket(host string) error {
	path := p.socket(host)
	if path == "" {
		return errors.New("ssh -G names no control path")
	}
	return os.Remove(path)
}

func (p hostProbes) Shared(host string) error {
	_, err := p.run(40*time.Second, append(sshArgs(host, "-o", "BatchMode=yes"), "true")...)
	return err
}

func (p hostProbes) Remote(host string) (repair.Remote, error) {
	script := `PATH="$HOME/.local/bin:$PATH"; if [ -x ` + remoteBin + ` ]; then echo installed; ` + remoteBin + ` version 2>/dev/null; echo; ` +
		remoteBin + ` service status 2>/dev/null; else echo missing; fi`
	out, err := p.run(40*time.Second, append(sshArgs(host, "-o", "BatchMode=yes"), script)...)
	if err != nil {
		return repair.Remote{}, err
	}
	return parseRemote(out), nil
}

// parseRemote reads what Remote's script printed: installed or missing, ym version's line, a blank line, then ym service status.
func parseRemote(out string) repair.Remote {
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	r := repair.Remote{Installed: len(lines) > 0 && lines[0] == "installed"}
	if !r.Installed || len(lines) < 2 {
		return r
	}
	rest := lines[1:]
	if rest[0] != "" {
		r.Version, rest = strings.TrimSpace(rest[0]), rest[1:]
	}
	var st struct {
		At      string `json:"at"`
		Running bool   `json:"running"`
	}
	if json.Unmarshal([]byte(strings.Join(rest, "\n")), &st) == nil {
		r.ServeRunning = st.Running
		r.ServeAt, _ = time.Parse(time.RFC3339Nano, st.At)
	}
	return r
}

func (hostProbes) Version() string { return sourceVersion() }

func (hostProbes) Install(host string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, self, "remote", "install", host).CombinedOutput()
	last := lastLine(string(out))
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, last)
	}
	return last, nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

type localProbes struct{}

func (localProbes) Foreign() (int, string, string) {
	dirs, err := ownedDirs()
	if err != nil {
		return 0, "", ""
	}
	n, first := foreignFiles(dirs, os.Geteuid())
	name := accountName(os.Geteuid())
	return n, first, chownFix(name, groupName(name), dirs)
}

func (localProbes) Procs() ([]repair.Proc, error) { return repair.ReadProcs() }
func (localProbes) UID() int                      { return os.Getuid() }
func (localProbes) Self() int                     { return os.Getpid() }

func (localProbes) StateDir() string {
	dir, _ := stateDir()
	abs, _ := filepath.Abs(dir)
	return abs
}

func (localProbes) ServeState(pid int) string {
	if dir := proc.ConfigRoot(pid, "YAMNAYA_STATE_DIR"); dir != "" {
		abs, _ := filepath.Abs(dir)
		return abs
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "yamnaya")
}

func (localProbes) Socket() string {
	if s := os.Getenv("YAMNAYA_YM_SOCKET"); s != "" {
		return s
	}
	return tmux.Server
}

func (localProbes) Launchd() repair.Launchd {
	switch {
	case runtime.GOOS != "darwin":
		return repair.Launchd{Why: "launchd is macOS only; ym service keep keeps ym serve through systemd here"}
	case os.Getenv("YAMNAYA_NO_SERVE") == "1":
		return repair.Launchd{Why: "YAMNAYA_NO_SERVE=1 keeps this ym from managing ym serve"}
	case !installedSelf():
		return repair.Launchd{Why: "this ym is not the ym on PATH, which launchd runs; run that ym's repair to check launchd"}
	}
	path, loaded, pid := launchdState()
	l := repair.Launchd{Loaded: loaded, PID: pid, Plist: path}
	if self, err := os.Executable(); err == nil {
		l.Want, _ = filepath.EvalSymlinks(self)
	}
	if raw, err := os.ReadFile(path); err == nil {
		if strs := plistString.FindAllSubmatch(raw, 2); len(strs) == 2 {
			l.Program = unescape(string(strs[1][1]))
		}
	}
	return l
}

func (localProbes) Keep() (string, error) { return keepLaunchd() }

func (localProbes) Kickstart() error {
	label, ok := keptServe()
	if !ok {
		return errors.New("launchd keeps no ym serve here")
	}
	return kickstart(label)
}

func (localProbes) Stop(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
	}
	return fmt.Errorf("pid %d still runs 10s after SIGTERM", pid)
}

func (localProbes) DeadSockets() []string {
	return deadSockets(tmuxDir(), filepath.Join("/tmp", fmt.Sprintf("ym-%d-*", os.Getuid())))
}

// tmuxDir is where tmux keeps this user's server sockets.
func tmuxDir() string {
	base := os.Getenv("TMUX_TMPDIR")
	if base == "" {
		base = "/tmp"
	}
	return filepath.Join(base, fmt.Sprintf("tmux-%d", os.Getuid()))
}

// deadSockets are the sockets in dir and matching glob that refuse a connection, so no server listens on them.
func deadSockets(dir, glob string) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "*"))
	more, _ := filepath.Glob(glob)
	var dead []string
	for _, p := range append(paths, more...) {
		info, err := os.Lstat(p)
		if err != nil || info.Mode()&fs.ModeSocket == 0 {
			continue
		}
		c, err := net.DialTimeout("unix", p, time.Second)
		if err == nil {
			c.Close()
			continue
		}
		if errors.Is(err, syscall.ECONNREFUSED) {
			dead = append(dead, p)
		}
	}
	return dead
}

func (localProbes) Remove(path string) error { return os.Remove(path) }

func (localProbes) Relays() ([]repair.Relay, error) {
	path, err := core.DefaultStorePath()
	if err != nil {
		return nil, err
	}
	st, err := core.OpenStore(path)
	if err != nil {
		return nil, err
	}
	defer st.Close()
	pending, err := st.PendingRelays()
	if err != nil {
		return nil, err
	}
	hosts, err := st.KnownHosts()
	if err != nil {
		return nil, err
	}
	self, _ := st.RelaySelf()
	reach := append(slices.Clone(self.Peers), self.Courier)
	for _, h := range hosts {
		reach = append(reach, h.Name)
	}
	var out []repair.Relay
	for _, r := range pending {
		out = append(out, repair.Relay{Seq: r.Seq, Dest: r.Dest, Refused: oneLine(r.Error), Forgotten: !slices.Contains(reach, r.Dest)})
	}
	return out, nil
}

func (localProbes) DropRelay(seq int64) error {
	path, err := core.DefaultStorePath()
	if err != nil {
		return err
	}
	st, err := core.OpenStore(path)
	if err != nil {
		return err
	}
	defer st.Close()
	_, err = st.DropRelay(seq)
	return err
}

// StaleLocks are the typing locks of panes ym's tmux server no longer has that nothing holds.
func (p localProbes) StaleLocks() []string {
	dir, err := stateDir()
	if err != nil {
		return nil
	}
	live := map[string]bool{}
	if out, err := exec.Command("tmux", "-L", p.Socket(), "list-panes", "-a", "-F", "#{pane_id}").Output(); err == nil {
		for _, id := range strings.Fields(string(out)) {
			live[strings.TrimPrefix(id, "%")] = true
		}
	} else if !errors.Is(err, exec.ErrNotFound) && !tmuxDown(err) {
		return nil
	}
	return staleLocks(dir, live)
}

// tmuxDown reports whether tmux failed because no server runs on the socket, so no pane is live.
func tmuxDown(err error) bool {
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return false
	}
	text := string(exit.Stderr)
	return strings.Contains(text, "no server running") || strings.Contains(text, "error connecting")
}

func staleLocks(dir string, live map[string]bool) []string {
	paths, _ := filepath.Glob(filepath.Join(dir, "type-*.lock"))
	var stale []string
	for _, path := range paths {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "type-"), ".lock")
		if live[id] {
			continue
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			continue
		}
		if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
			stale = append(stale, path)
		}
		f.Close()
	}
	return stale
}

// trouble is the board header's short note on what ym repair would fix here, read at most every 30 seconds.
var trouble = sync.OnceValue(func() func() string {
	var mu sync.Mutex
	var at time.Time
	var last string
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < 30*time.Second {
			return last
		}
		at, last = time.Now(), localTrouble(localProbes{})
		return last
	}
})

func localTrouble(p repair.LocalProbes) string {
	var found []string
	if procs, err := p.Procs(); err == nil && p.UID() != 0 && len(repair.RootYM(procs, p.Socket())) > 0 {
		found = append(found, "root ym processes")
	}
	if n, _, _ := p.Foreign(); n > 0 {
		found = append(found, "root-owned files")
	}
	if len(found) == 0 {
		return ""
	}
	return "⚠ " + strings.Join(found, ", ")
}
