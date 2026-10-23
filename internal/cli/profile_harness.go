package cli

import (
	"path/filepath"
	"strconv"
	"sync"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type profileHarness struct {
	name, dir string
	cache     core.ScanCache
	mu        sync.Mutex
	adapters  map[string]core.Harness
}

func (h *profileHarness) Name() string { return h.name }
func (h *profileHarness) all() ([]profile.Profile, map[string]core.Harness) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ps, _ := profile.Load(h.dir)
	if h.adapters == nil {
		h.adapters = map[string]core.Harness{}
	}
	out := map[string]core.Harness{}
	for _, p := range ps {
		if p.Harness != h.name {
			continue
		}
		key := p.ID + "\x00" + p.Root
		a := h.adapters[key]
		if a == nil {
			if h.name == claude.Name {
				cl := claude.New()
				cl.Dir = p.Root
				cl.Cache = h.cache
				a = cl
			} else {
				cx := codex.New()
				cx.Home = p.Root
				cx.StateHome = p.SQLiteRoot
				cx.Cache = h.cache
				a = cx
			}
			h.adapters[key] = a
		}
		out[p.ID] = a
	}
	return ps, out
}
func (h *profileHarness) forProc(p proc.Proc) (profile.Profile, core.Harness) {
	ps, as := h.all()
	root := proc.ConfigRoot(p.PID, profile.EnvKey(h.name))
	if root != "" {
		if real, err := filepath.EvalSymlinks(root); err == nil {
			root = real
		}
	}
	for _, q := range ps {
		if q.Harness == h.name && root != "" && q.Root == root {
			return q, as[q.ID]
		}
	}
	if root != "" {
		return profile.Profile{}, nil
	}
	q, _ := profile.Find(ps, h.name, "")
	return q, as[q.ID]
}
func (h *profileHarness) Owns(p proc.Proc) bool {
	if p.Command() != h.name && !(h.name == "claude" && p.Command() == "claude bg-spare") {
		return false
	}
	_, a := h.forProc(p)
	return a != nil && a.Owns(p)
}
func (h *profileHarness) SessionID(p proc.Proc) string {
	_, a := h.forProc(p)
	if a == nil {
		return ""
	}
	return a.SessionID(p)
}
func (h *profileHarness) Discover(ps []proc.Proc) []core.Session {
	groups := map[string][]proc.Proc{}
	profiles := map[string]profile.Profile{}
	adapters := map[string]core.Harness{}
	for _, p := range ps {
		q, a := h.forProc(p)
		groups[q.ID] = append(groups[q.ID], p)
		profiles[q.ID] = q
		adapters[q.ID] = a
	}
	// Claude's background jobs also need their root's discovery even without a terminal process.
	all, as := h.all()
	for _, q := range all {
		if q.Harness == h.name {
			profiles[q.ID] = q
			adapters[q.ID] = as[q.ID]
		}
	}
	var out []core.Session
	for id, q := range profiles {
		for _, s := range adapters[id].Discover(groups[id]) {
			s.Profile = q.ID
			s.ProfileLabel = q.Label
			s.Account = q.Account
			s.ConfigRoot = q.Root
			out = append(out, s)
		}
	}
	return out
}
func (h *profileHarness) Resume(id string) []string {
	ps, as := h.all()
	for _, q := range ps {
		if q.Harness != h.name {
			continue
		}
		a := as[q.ID]
		if t, ok := a.(interface{ Transcript(string) string }); ok && t.Transcript(id) != "" {
			return profileNativeResume(q, a.Resume(id))
		}
	}
	q, _ := profile.Find(ps, h.name, "")
	return profileNativeResume(q, as[q.ID].Resume(id))
}
func (h *profileHarness) ResumeProfile(id, selected string) []string {
	ps, as := h.all()
	q, err := profile.Find(ps, h.name, selected)
	if err != nil {
		return nil
	}
	return profileNativeResume(q, as[q.ID].Resume(id))
}
func (h *profileHarness) ParsePrompt(screen string) (core.Prompt, bool) {
	_, as := h.all()
	return as["default"].ParsePrompt(screen)
}
func (h *profileHarness) Composer(screen string) (string, bool) {
	_, as := h.all()
	return as["default"].Composer(screen)
}
func (h *profileHarness) Waiting(pid int) bool {
	_, a := h.forProc(proc.Proc{PID: pid})
	w, ok := a.(core.Waiter)
	return ok && w.Waiting(pid)
}
func (h *profileHarness) Message(s core.Session, text string, owner bool) error {
	_, as := h.all()
	m, ok := as[profile.ID(s.Profile)].(core.Messenger)
	if !ok {
		return core.ErrNotFound
	}
	return m.Message(s, text, owner)
}
func (h *profileHarness) Successor(id, near string) string {
	_, as := h.all()
	for _, a := range as {
		if ct, ok := a.(core.Continuer); ok {
			if s := ct.Successor(id, near); s != "" {
				return s
			}
		}
	}
	return ""
}
func (h *profileHarness) Generation() uint64 {
	_, as := h.all()
	var n uint64
	for _, a := range as {
		if c, ok := a.(core.Changing); ok {
			n += c.Generation()
		}
	}
	return n
}

func profileNativeResume(p profile.Profile, args []string) []string {
	if p.DefaultConstraint() != nil {
		return nil
	}
	if p.Harness == "codex" {
		args = append(args[:len(args)-1], append([]string{"--no-daemon"}, args[len(args)-1:]...)...)
		if p.ID != "default" {
			at := len(args) - 1
			args = append(args[:at], append([]string{"-c", "sqlite_home=" + strconv.Quote(p.Root)}, args[at:]...)...)
		}
	}
	return p.Argv(args)
}
