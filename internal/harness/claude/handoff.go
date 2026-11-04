package claude

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// dialogTail matches the quota dialog read with no spaces or line breaks, as a narrow pane wraps it anywhere.
var dialogTail = regexp.MustCompile(`^Whatdoyouwanttodo\?1\.Stopandwaitforlimittoreset2\.(.*)3\.SwitchtousagecreditsEntertoconfirm·Esctocancel$`)

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
		return append(slices.Clone(lines[:start]), "What do you want to do?", "1. Stop and wait for limit to reset", "2. "+m[1], "3. Switch to usage credits", "Enter to confirm · Esc to cancel")
	}
	return lines
}

func (a *Adapter) QuotaDialog(text string) bool {
	lines := quotaDialogLines(text)
	if len(lines) < 6 {
		return false
	}
	end := lines[len(lines)-5:]
	if end[0] != "What do you want to do?" || end[1] != "1. Stop and wait for limit to reset" || !strings.HasPrefix(end[2], "2. ") || end[3] != "3. Switch to usage credits" || end[4] != "Enter to confirm · Esc to cancel" {
		return false
	}
	above := lines[max(0, len(lines)-9) : len(lines)-5]
	for i := range above {
		joined := strings.Join(strings.Fields(strings.TrimPrefix(strings.TrimSpace(strings.Join(above[i:], "")), "⎿")), "")
		if strings.HasPrefix(joined, "You'vehityourweeklylimit") || strings.HasPrefix(joined, "You'vehityourlimit") {
			return true
		}
	}
	return false
}

// dialogRule is the dialog's top rule, which can carry the effort level.
var dialogRule = regexp.MustCompile(`^▔+(\s.*\s▔+)?$`)

// silentQuotaDialog reports the quota dialog as Claude shows it the moment it starts on a used-up login: no turn ran, so no hit-limit line
// sits above it, only the dialog's own top rule. The rule tells it from those lines quoted in scrollback or another dialog's options.
func silentQuotaDialog(text string) bool {
	lines := quotaDialogLines(text)
	if len(lines) < 6 {
		return false
	}
	end := lines[len(lines)-5:]
	rule := dialogRule.MatchString(lines[len(lines)-6])
	return rule && end[0] == "What do you want to do?" && end[1] == "1. Stop and wait for limit to reset" && strings.HasPrefix(end[2], "2. ") && end[3] == "3. Switch to usage credits" && end[4] == "Enter to confirm · Esc to cancel"
}

// quotaFooter matches the footer read with no spaces or line breaks.
var quotaFooter = regexp.MustCompile(`^⚠Usagelimitreached·limitresets(.+?)Continuingautomaticallyat.+?·esctocancel`)

// hitLimitLine is the hit-limit line read with no spaces: when the limit resets, and the one place a clock-only reset says which zone it means.
var hitLimitLine = regexp.MustCompile(`You['’]vehityour(?:weekly)?limit·resets((?:[A-Z][a-z]{2}\d{1,2},)?\d{1,2}(?::\d{2})?(?:am|pm))(?:\(([^)]+)\))?`)

// footerStop reads the footer Claude shows when a turn hits the limit, and what it says about the reset. It sits below the input box, under
// the box's closing rule: anything typed or pasted is above that rule, and anything a reply prints is above the box.
func footerStop(text string, now time.Time) (time.Time, bool) {
	lines := screen.Lines(text)
	box := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.HasPrefix(lines[i], "❯") {
			box = i
			break
		}
	}
	if box < 0 {
		return time.Time{}, false
	}
	closing := -1
	for i := box + 1; i < len(lines); i++ {
		if rule := strings.TrimSpace(lines[i]); rule != "" && strings.Trim(rule, "─") == "" {
			closing = i
		}
	}
	if closing < 0 {
		return time.Time{}, false
	}
	m := quotaFooter.FindStringSubmatch(strings.ReplaceAll(strings.Join(lines[closing+1:], ""), " ", ""))
	if m == nil {
		return time.Time{}, false
	}
	return readReset(text, m[1], now), true
}

// hitLine is the last hit-limit line on the screen, nil with none.
func hitLine(text string) []string {
	all := hitLimitLine.FindAllStringSubmatch(strings.Join(strings.Fields(text), ""), -1)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// readReset reads a reset on the screen, in the zone the screen's hit-limit line names when it names one itself.
func readReset(text, reset string, now time.Time) time.Time {
	if m := hitLine(text); m != nil && m[2] != "" && !strings.Contains(reset, "(") {
		reset += "(" + m[2] + ")"
	}
	return parseQuotaReset(reset, now)
}

// dialogReset is when the quota dialog says the limit resets: the time its second option names when it names one, else the hit-limit line's.
func dialogReset(text string, now time.Time) time.Time {
	lines := quotaDialogLines(text)
	if len(lines) < 5 || lines[len(lines)-5] != "What do you want to do?" {
		return time.Time{}
	}
	if _, at, ok := strings.Cut(strings.ReplaceAll(lines[len(lines)-3], " ", ""), "automaticallyat"); ok {
		return readReset(text, at, now)
	}
	if m := hitLine(text); m != nil {
		return readReset(text, m[1], now)
	}
	return time.Time{}
}

// lateRead is how long past a bare clock a screen read now still means that clock, not its next occurrence.
const lateRead = time.Hour

var quotaClock = regexp.MustCompile(`^(?:([A-Z][a-z]{2})(\d{1,2}),)?(\d{1,2})(?::(\d{2}))?(am|pm)(?:\((.+)\))?$`)

// parseQuotaReset reads when Claude says a limit resets, with spaces removed: a clock time, perhaps with a date and a zone. A bare time is
// its next occurrence in the zone named, else this machine's, which is the one Claude prints in, but one within the last hour was read late and
// has passed, as has a date within the last half year. Zero when it cannot be read.
func parseQuotaReset(text string, now time.Time) time.Time {
	m := quotaClock.FindStringSubmatch(text)
	if m == nil {
		return time.Time{}
	}
	loc := time.Local
	if m[6] != "" {
		named, err := time.LoadLocation(m[6])
		if err != nil {
			return time.Time{}
		}
		loc = named
	}
	hour, _ := strconv.Atoi(m[3])
	minute, _ := strconv.Atoi(m[4])
	if hour < 1 || hour > 12 || minute > 59 {
		return time.Time{}
	}
	hour %= 12
	if m[5] == "pm" {
		hour += 12
	}
	local := now.In(loc)
	if m[1] != "" {
		month, err := time.Parse("Jan", m[1])
		if err != nil {
			return time.Time{}
		}
		day, _ := strconv.Atoi(m[2])
		at := time.Date(local.Year(), month.Month(), day, hour, minute, 0, 0, loc)
		if at.Before(now.AddDate(0, -6, 0)) {
			at = at.AddDate(1, 0, 0)
		}
		return at
	}
	at := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if at.Before(now.Add(-lateRead)) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// SilentQuotaStop proves a session is held on its limit by what its screen shows: the rule-topped dialog over a turn that is over, whether
// it ended normally, as starting on a used-up login leaves it, or on a quota error that scrolled away; or the footer under its input box,
// which reads idle and from which Claude carries on by itself at the reset. stopped is false when the screen shows neither.
func (a *Adapter) SilentQuotaStop(s core.Session, text string) (core.QuotaStop, bool, error) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	reset, footer := footerStop(text, now())
	if !footer {
		reset = dialogReset(text, now())
		if !silentQuotaDialog(text) {
			return core.QuotaStop{Reset: reset}, false, nil
		}
	}
	stop := core.QuotaStop{Reset: reset, SelfContinues: footer}
	st, ok := a.state(s.PID)
	if !ok || st.SessionID != s.ID || st.Status != "waiting" && (!footer || st.Status != "idle") {
		return stop, true, fmt.Errorf("native waiting state is unreadable or changed")
	}
	path := a.transcript(s.Cwd, s.ID)
	sc, err := a.handoffScan(path)
	if err != nil {
		return stop, true, err
	}
	quota := sc.LastStop == "stop_sequence" && !sc.QuotaAt.IsZero() && !sc.QuotaSidechain
	if sc.SessionID != s.ID || sc.LastType != "assistant" || !sc.Stopped || sc.LastStop != "end_turn" && !quota || sc.UserAt.After(sc.LastAt) {
		return stop, true, fmt.Errorf("the turn behind the quota stop has not ended")
	}
	if err = a.handoffChildren(s, path); err != nil {
		return stop, true, err
	}
	stop.At = sc.LastAt
	if run := a.processStart(s); !run.IsZero() && run.Before(stop.At) {
		// A process older than the stop has shown it since: the time it names is the next after the stop, however late this reads it.
		stop.Dated = true
		if footer {
			stop.Reset, _ = footerStop(text, stop.At.Add(lateRead))
		} else {
			stop.Reset = dialogReset(text, stop.At.Add(lateRead))
		}
	}
	return stop, true, nil
}

// LastTurnAt is when the conversation's last turn ended, zero when that cannot be read.
func (a *Adapter) LastTurnAt(cwd, id string) time.Time {
	sc, err := a.handoffScan(a.transcript(cwd, id))
	if err != nil || sc.LastType != "assistant" || !sc.Stopped || sc.LastStop != "end_turn" {
		return time.Time{}
	}
	return sc.LastAt
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

// processStart is when s's process started, zero when that cannot be read.
func (a *Adapter) processStart(s core.Session) time.Time {
	list := a.Procs
	if list == nil {
		list = proc.List
	}
	ps, err := list()
	if err != nil {
		return time.Time{}
	}
	return a.startedIn(proc.Index(ps), s)
}

func (a *Adapter) startedIn(table proc.Table, s core.Session) time.Time {
	p, ok := table[s.PID]
	if !ok {
		return time.Time{}
	}
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	return p.Started(now())
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
	children, err := a.stoppedChildren(path, s.ID, a.startedIn(table, s))
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
