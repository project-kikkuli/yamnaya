package signals

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/discover"
)

// Session is a harness running in a tmux pane that no ym window tracks.
type Session struct {
	discover.Session
	Permission *Permission
}

// Label is the session's status in a few words.
func (s Session) Label(now time.Time) string {
	if s.Permission != nil {
		return "needs permission · " + s.Permission.String()
	}
	switch s.Status {
	case discover.Running:
		return "running"
	case discover.NeedsYou:
		if s.WaitingFor != "" {
			return "needs you · " + s.WaitingFor
		}
		return "needs you"
	case discover.YourTurn:
		return "waiting on you"
	}
	if s.Since.IsZero() {
		return "idle"
	}
	return "idle " + Duration(now.Sub(s.Since))
}

// Loud reports whether the session wants the owner now.
func (s Session) Loud() bool {
	return s.Permission != nil || s.Status == discover.NeedsYou || s.Status == discover.YourTurn
}

func (r *Reader) permission(target string) *Permission {
	screen, err := r.Tmux.CapturePane(target)
	if err != nil {
		return nil
	}
	if perm, ok := ParsePermission(screen); ok {
		return &perm
	}
	return nil
}

// attachSessions hangs each discovered session on the ym window that runs it, and lists the rest.
func (r *Reader) attachSessions(snap *Snapshot) error {
	if r.Discover == nil {
		return nil
	}
	found, err := r.Discover()
	if err != nil {
		return err
	}
	for _, d := range found {
		if w := snap.windowFor(d); w != nil {
			d := d
			w.Discovered = &d
			if d.Status == discover.Running {
				w.LastActive = time.Now()
			} else if !d.Since.IsZero() {
				w.LastActive = d.Since
			}
			continue
		}
		s := Session{Session: d}
		if d.Status == discover.Running || d.Status == discover.NeedsYou {
			s.Permission = r.permission(d.Pane)
		}
		snap.Sessions = append(snap.Sessions, s)
	}
	return nil
}

func (s *Snapshot) windowFor(d discover.Session) *Window {
	for i := range s.Projects {
		for j := range s.Projects[i].Windows {
			w := &s.Projects[i].Windows[j]
			if (d.ID != "" && w.SessionID == d.ID) || (w.Alive && w.TmuxTarget == d.WindowID) {
				return w
			}
		}
	}
	return nil
}
