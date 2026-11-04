package claude

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// dialogTail matches the quota dialog read with no spaces or line breaks, as a narrow pane wraps it anywhere.
var dialogTail = regexp.MustCompile(`^Whatdoyouwanttodo\?1\.Stopandwaitforlimittoreset2\.Waithere,thencontinueautomaticallyat(.*)3\.SwitchtousagecreditsEntertoconfirm·Esctocancel$`)

// quotaDialogLines are the screen's lines with a quota dialog at the bottom put back as the five lines it is at full width, however the
// pane wrapped it, so what is above it can be read as it stands.
func quotaDialogLines(text string) []string {
	var lines []string
	for _, line := range screen.Lines(text) {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "❯")); line != "" {
			lines = append(lines, line)
		}
	}
	for start := len(lines) - 1; start >= 0 && start >= len(lines)-24; start-- {
		if !strings.HasPrefix("What do you want to do?", lines[start]) && !strings.HasPrefix(lines[start], "What") {
			continue
		}
		joined := strings.NewReplacer(" ", "", "❯", "").Replace(strings.Join(lines[start:], ""))
		m := dialogTail.FindStringSubmatch(joined)
		if m == nil {
			continue
		}
		return append(slices.Clone(lines[:start]), "What do you want to do?", "1. Stop and wait for limit to reset", "2. Wait here, then continue automatically at "+m[1], "3. Switch to usage credits", "Enter to confirm · Esc to cancel")
	}
	return lines
}

func (a *Adapter) QuotaDialog(text string) bool {
	lines := quotaDialogLines(text)
	if len(lines) < 6 {
		return false
	}
	end := lines[len(lines)-5:]
	if end[0] != "What do you want to do?" || end[1] != "1. Stop and wait for limit to reset" || !strings.HasPrefix(end[2], "2. Wait here, then continue automatically at ") || end[3] != "3. Switch to usage credits" || end[4] != "Enter to confirm · Esc to cancel" {
		return false
	}
	for _, line := range lines[max(0, len(lines)-9) : len(lines)-5] {
		line = strings.TrimSpace(strings.TrimPrefix(line, "⎿"))
		if strings.HasPrefix(line, "You've hit your weekly limit") || strings.HasPrefix(line, "You've hit your limit") {
			return true
		}
	}
	return false
}

// silentQuotaDialog reports the quota dialog as Claude shows it the moment it starts on a used-up login: no turn ran, so no hit-limit line
// sits above it, only the dialog's own top rule. The rule tells it from those lines quoted in scrollback or another dialog's options.
func silentQuotaDialog(text string) bool {
	lines := quotaDialogLines(text)
	if len(lines) < 6 {
		return false
	}
	end := lines[len(lines)-5:]
	rule := strings.Trim(lines[len(lines)-6], "▔") == ""
	return rule && end[0] == "What do you want to do?" && end[1] == "1. Stop and wait for limit to reset" && strings.HasPrefix(end[2], "2. Wait here, then continue automatically at ") && end[3] == "3. Switch to usage credits" && end[4] == "Enter to confirm · Esc to cancel"
}

// SilentQuotaStop proves a session is held at the rule-topped quota dialog on a turn that is over, whether it ended normally, as starting on
// a used-up login leaves it, or on a quota error that scrolled away. stopped is false when text is not that dialog.
func (a *Adapter) SilentQuotaStop(s core.Session, text string) (at time.Time, stopped bool, err error) {
	if !silentQuotaDialog(text) || a.QuotaDialog(text) {
		return time.Time{}, false, nil
	}
	st, ok := a.state(s.PID)
	if !ok || st.SessionID != s.ID || st.Status != "waiting" {
		return time.Time{}, true, fmt.Errorf("native waiting state is unreadable or changed")
	}
	path := a.transcript(s.Cwd, s.ID)
	sc, err := a.handoffScan(path)
	if err != nil {
		return time.Time{}, true, err
	}
	quota := sc.LastStop == "stop_sequence" && !sc.QuotaAt.IsZero() && !sc.QuotaSidechain
	if sc.SessionID != s.ID || sc.LastType != "assistant" || !sc.Stopped || sc.LastStop != "end_turn" && !quota || sc.UserAt.After(sc.LastAt) {
		return time.Time{}, true, fmt.Errorf("the turn behind the quota dialog has not ended")
	}
	if err = a.handoffChildren(s, path); err != nil {
		return time.Time{}, true, err
	}
	return sc.LastAt, true, nil
}

func (a *Adapter) handoffScan(path string) (scan, error) {
	root, err := filepath.EvalSymlinks(a.Dir)
	if err != nil {
		return scan{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return scan{}, err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || !strings.HasPrefix(rel, "projects"+string(filepath.Separator)) {
		return scan{}, fmt.Errorf("native transcript is outside selected profile history")
	}
	info, err := os.Stat(path)
	if err != nil {
		return scan{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return scan{}, err
	}
	f.Close()
	sc := a.scan(path)
	if sc.Invalid || sc.Offset == 0 || sc.Offset != info.Size() || len(sc.Calls) != 0 {
		return sc, fmt.Errorf("native transcript is incomplete or has outstanding tools")
	}
	return sc, nil
}

func stoppedChild(sc scan) bool {
	return sc.HandedBack || sc.LastType == "assistant" && sc.Stopped && !sc.UserAt.After(sc.LastAt) && (sc.LastStop == "end_turn" || sc.LastStop == "stop_sequence" && !sc.QuotaAt.IsZero())
}

// stoppedChildren proves every subagent of the transcript at path has stopped. An in-process subagent dies with its process, so one
// whose transcript was last written before run, when the running process started, is over however it ended.
func (a *Adapter) stoppedChildren(path, parent string, run time.Time) (map[string]bool, error) {
	dir := strings.TrimSuffix(path, ".jsonl") + "/subagents"
	children := map[string]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if os.IsNotExist(err) && path == dir {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("native child path is a symlink")
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".meta.json") {
			_, err := os.Stat(strings.TrimSuffix(path, ".meta.json") + ".jsonl")
			return err
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		over := info.ModTime().Before(run)
		var child scan
		if over {
			child = a.scan(path)
		} else if child, err = a.handoffScan(path); err != nil {
			return err
		}
		id := strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), ".jsonl"), "agent-")
		if child.SessionID == "" || child.SessionID != parent && child.SessionID != id || child.AgentID != "" && child.AgentID != id {
			return fmt.Errorf("native child transcript identity mismatch")
		}
		if !over && !stoppedChild(child) {
			return fmt.Errorf("child native turn is active or unknown")
		}
		children[id] = true
		if child.SessionID != parent {
			children[child.SessionID] = true
		}
		if child.AgentID != "" {
			children[child.AgentID] = true
		}
		return nil
	})
	return children, err
}

func (a *Adapter) InspectHandoff(s core.Session) (time.Time, error) {
	st, ok := a.state(s.PID)
	if !ok || st.SessionID != s.ID || st.Status != "waiting" {
		return time.Time{}, fmt.Errorf("native waiting state is unreadable or changed")
	}
	path := a.transcript(s.Cwd, s.ID)
	sc, err := a.handoffScan(path)
	if err != nil {
		return time.Time{}, err
	}
	if sc.SessionID != s.ID || sc.QuotaAt.IsZero() || sc.QuotaSidechain || !sc.Stopped || sc.LastStop != "stop_sequence" || sc.LastType != "assistant" || sc.UserAt.After(sc.QuotaAt) {
		return time.Time{}, fmt.Errorf("main turn has no matching terminal subscription quota error")
	}
	return sc.QuotaAt, a.handoffChildren(s, path)
}

// handoffChildren proves nothing the session spawned is still working.
func (a *Adapter) handoffChildren(s core.Session, path string) error {
	list := a.Procs
	if list == nil {
		list = proc.List
	}
	ps, err := list()
	if err != nil {
		return err
	}
	table := proc.Index(ps)
	var run time.Time
	if p, ok := table[s.PID]; ok {
		now := time.Now
		if a.Now != nil {
			now = a.Now
		}
		run = p.Started(now())
	}
	children, err := a.stoppedChildren(path, s.ID, run)
	if err != nil {
		return err
	}
	rootOf := a.RootOf
	if rootOf == nil {
		rootOf = proc.ConfigRoot
	}
	selected, err := filepath.EvalSymlinks(a.Dir)
	if err != nil {
		return err
	}
	inspected := map[int]bool{}
	for changed := true; changed; {
		changed = false
		for _, p := range ps {
			if p.PID == s.PID || inspected[p.PID] || p.Command() != "claude" && p.Command() != "claude bg-spare" {
				continue
			}
			related := children[argSessionID(p.Args)]
			for _, ancestor := range table.Ancestors(p.PID) {
				if ancestor.PID == s.PID {
					related = true
				}
			}
			native, readable := a.state(p.PID)
			if readable && children[native.SessionID] {
				related = true
			}
			if !related {
				continue
			}
			root := rootOf(p.PID, "CLAUDE_CONFIG_DIR")
			if root == "" {
				home, _ := os.UserHomeDir()
				root = filepath.Join(home, ".claude")
			}
			physical, err := filepath.EvalSymlinks(root)
			if err != nil || physical != selected || !readable || native.Status != "idle" {
				return fmt.Errorf("native child pid %d is active, outside the selected root, or unknown", p.PID)
			}
			ownPath := a.transcript(native.Cwd, native.SessionID)
			own, err := a.handoffScan(ownPath)
			if err != nil || own.SessionID != native.SessionID || !stoppedChild(own) {
				return fmt.Errorf("native child pid %d has no proven completed turn", p.PID)
			}
			nested, err := a.stoppedChildren(ownPath, native.SessionID, time.Time{})
			if err != nil {
				return err
			}
			for id := range nested {
				children[id] = true
			}
			inspected[p.PID], changed = true, true
		}
	}
	return nil
}
