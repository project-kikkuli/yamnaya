package signals

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

// IdleAfter is how long a transcript stays quiet before its window reads as idle.
const IdleAfter = 2 * time.Minute

type State int

const (
	Running State = iota
	Idle
	Asking
	Dead
)

type Window struct {
	store.Window
	Alive      bool
	Open       int
	LastActive time.Time
}

func (w Window) State(now time.Time) State {
	switch {
	case w.Open > 0:
		return Asking
	case !w.Alive:
		return Dead
	case !w.LastActive.IsZero() && now.Sub(w.LastActive) >= IdleAfter:
		return Idle
	}
	return Running
}

func (w Window) Label(now time.Time) string {
	switch w.State(now) {
	case Asking:
		if !w.Alive {
			return "asks you (dead)"
		}
		return "asks you"
	case Dead:
		return "dead"
	case Idle:
		return "idle " + Duration(now.Sub(w.LastActive))
	}
	return "running"
}

type Project struct {
	store.Project
	Open    int
	Windows []Window
}

func (p Project) Dead() int {
	n := 0
	for _, w := range p.Windows {
		if !w.Alive {
			n++
		}
	}
	return n
}

// Window finds an open window by name; an empty name means the manager.
func (p Project) Window(name string) (Window, bool) {
	for _, w := range p.Windows {
		if w.Name == name || (name == "" && w.Role == store.RoleManager) {
			return w, true
		}
	}
	return Window{}, false
}

type Snapshot struct {
	Projects  []Project
	Unmanaged []tmux.WindowInfo
}

func (s Snapshot) Project(name string) (Project, bool) {
	for _, p := range s.Projects {
		if p.Name == name {
			return p, true
		}
	}
	return Project{}, false
}

type Reader struct {
	Store       *store.Store
	Tmux        tmux.Client
	Transcripts *Transcripts
}

func NewReader(s *store.Store, t tmux.Client) *Reader {
	return &Reader{Store: s, Tmux: t, Transcripts: NewTranscripts()}
}

// Snapshot joins every active project's stored windows and items with live tmux state and transcript activity.
func (r *Reader) Snapshot() (Snapshot, error) {
	projects, err := r.Store.Projects(store.ProjectActive)
	if err != nil {
		return Snapshot{}, err
	}
	live, err := r.Tmux.Windows()
	if err != nil {
		return Snapshot{}, err
	}
	var snap Snapshot
	tracked := map[string]bool{}
	for _, p := range projects {
		windows, err := r.Store.OpenWindows(p.Name)
		if err != nil {
			return snap, err
		}
		if err := ResolveCodexSessions(r.Store, p, windows); err != nil {
			return snap, err
		}
		total, byWindow, err := r.Store.OpenItemCounts(p.Name)
		if err != nil {
			return snap, err
		}
		sp := Project{Project: p, Open: total}
		for _, w := range windows {
			tracked[w.TmuxTarget] = true
			open := byWindow[w.Name]
			if w.Role == store.RoleManager {
				open += byWindow[""]
			}
			sp.Windows = append(sp.Windows, Window{
				Window:     w,
				Alive:      Live(w, live),
				Open:       open,
				LastActive: r.Transcripts.LastActive(w.Harness, w.SessionID),
			})
		}
		snap.Projects = append(snap.Projects, sp)
	}
	for _, info := range live {
		if !tracked[info.ID] && !strings.HasPrefix(info.Session, "ym-") {
			snap.Unmanaged = append(snap.Unmanaged, info)
		}
	}
	sort.Slice(snap.Unmanaged, func(i, j int) bool {
		a, b := snap.Unmanaged[i], snap.Unmanaged[j]
		if a.Session != b.Session {
			return a.Session < b.Session
		}
		return a.Index < b.Index
	})
	return snap, nil
}

// Live reports whether w's tmux window still runs; a reused tmux id carries another tag and does not count.
// Managed windows must also sit in their project's session; adopted ones stay wherever they were.
func Live(w store.Window, live map[string]tmux.WindowInfo) bool {
	info, ok := live[w.TmuxTarget]
	inPlace := !w.Managed || info.Session == tmux.SessionName(w.Project)
	return ok && info.Tag == strconv.FormatInt(w.ID, 10) && inPlace && !info.Dead
}

// ResolveCodexSessions records Codex session ids for open windows whose thread Codex has since written.
func ResolveCodexSessions(s *store.Store, p store.Project, windows []store.Window) error {
	var claimed map[string]bool
	for i, w := range windows {
		if w.Harness != harness.Codex || w.SessionID != "" || !w.Managed {
			continue
		}
		if claimed == nil {
			var err error
			if claimed, err = s.ClaimedSessionIDs(harness.Codex); err != nil {
				return err
			}
		}
		id, err := harness.CodexSessionID(p.Cwd, w.CreatedAt, w.Prompt, claimed)
		if err != nil {
			return err
		}
		if id == "" {
			continue
		}
		if err := s.SetSessionID(w.ID, id); err != nil {
			return err
		}
		claimed[id] = true
		windows[i].SessionID = id
	}
	return nil
}

func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}
