package cli

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func underPID(table proc.Table, p proc.Proc, pid int) bool {
	if p.PID == pid {
		return true
	}
	for _, a := range table.Ancestors(p.PID) {
		if a.PID == pid {
			return true
		}
	}
	return false
}
func (l launcher) effectiveRoot(pid int) string {
	rootOf := l.rootOf
	if rootOf == nil {
		rootOf = proc.ConfigRoot
	}
	root := rootOf(pid, "CODEX_HOME")
	if root == "" {
		ps, _ := profile.Load(filepath.Dir(l.records.Dir))
		p, _ := profile.Find(ps, "codex", "")
		root = p.Root
	}
	if actual, err := filepath.EvalSymlinks(root); err == nil {
		return actual
	}
	return root
}
func (l launcher) nativeLifecycle(rec launch.Record, dest profile.Profile, allowSource bool) ([]proc.Proc, error) {
	if rec.Harness != "codex" {
		return nil, nil
	}
	list := proc.List
	if l.core != nil && l.core.Procs != nil {
		list = l.core.Procs
	}
	ps, err := list()
	if err != nil {
		return nil, fmt.Errorf("cannot inspect native lifecycles: %w", err)
	}
	records, err := l.records.All()
	if err != nil {
		return nil, err
	}
	byKey := map[string]launch.Record{}
	for _, r := range records {
		byKey[r.Key] = r
	}
	table := proc.Index(ps)
	rootOf := l.rootOf
	if rootOf == nil {
		rootOf = proc.ConfigRoot
	}
	related := map[string]bool{rec.Session: true}
	if rec.Harness == "codex" {
		for id := range recordCodex(rec).ConversationArtifacts(rec.Session) {
			related[id] = true
		}
	}
	var owned []proc.Proc
	for _, p := range ps {
		if p.Command() != "codex" {
			continue
		}
		root := l.effectiveRoot(p.PID)
		if root != physicalProfileRoot(rec.ConfigRoot) && root != physicalProfileRoot(dest.Root) {
			continue
		}
		key := rootOf(p.PID, "YAMNAYA_LAUNCH_KEY")
		r, managed := byKey[key]
		if !managed || !r.Dedicated || r.NativePID == 0 || !underPID(table, p, r.NativePID) {
			return nil, fmt.Errorf("external/shared Codex lifecycle pid %d can prepare a stale resume in profile root; native handoff is blocked", p.PID)
		}
		if related[r.Session] {
			if !allowSource || r.Key != rec.Key || root != physicalProfileRoot(rec.ConfigRoot) {
				return nil, fmt.Errorf("codex conversation lifecycle pid %d has not drained", p.PID)
			}
			owned = append(owned, p)
			continue
		}
		if p.PID != r.NativePID && !slices.Contains(p.Params(), "app-server") {
			return nil, fmt.Errorf("untracked native resumer pid %d under another session; handoff blocked", p.PID)
		}
		for _, arg := range p.Params() {
			if strings.Contains(arg, rec.Session) {
				return nil, fmt.Errorf("pending native resumer pid %d can reopen this conversation", p.PID)
			}
		}
	}
	return owned, nil
}
func (l launcher) drainNative(children []proc.Proc, root int) error {
	signal := proc.Signal
	if l.core != nil {
		signal = l.core.SignalFunc()
	}
	for _, p := range children {
		if p.PID != root {
			if err := signal(p.PID, syscall.SIGTERM); err != nil && err != syscall.ESRCH {
				return err
			}
		}
	}
	return nil
}
func (l launcher) awaitDrain(children []proc.Proc) error {
	if len(children) == 0 {
		return nil
	}
	list := proc.List
	if l.core != nil && l.core.Procs != nil {
		list = l.core.Procs
	}
	expected := map[int]bool{}
	for _, p := range children {
		expected[p.PID] = true
	}
	for end := time.Now().Add(3 * time.Second); ; time.Sleep(25 * time.Millisecond) {
		ps, err := list()
		if err != nil {
			return err
		}
		var left []int
		for _, p := range ps {
			if expected[p.PID] {
				left = append(left, p.PID)
			}
		}
		if len(left) == 0 {
			return nil
		}
		if !time.Now().Before(end) {
			return fmt.Errorf("owned Codex native lifecycle did not exit: pids %v; handoff blocked", left)
		}
	}
}

func physicalProfileRoot(root string) string {
	if path, err := filepath.EvalSymlinks(root); err == nil {
		return path
	}
	return filepath.Clean(root)
}
