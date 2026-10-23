package core

import (
	"sync"
	"time"
)

// promptCache keeps the dialog read off a waiting session's screen while the session still waits on the same thing: a session's
// harness rewrites its waiting state for each new dialog, so a thousand waiting sessions cost no thousand screen reads a second.
type promptCache struct {
	mu   sync.Mutex
	seen map[string]shownPrompt
}

type shownPrompt struct {
	since  time.Time
	why    string
	pane   string
	at     time.Time
	prompt *Prompt
}

// promptRecheck is how long a read screen stands for a session whose waiting state did not change.
const promptRecheck = 10 * time.Second

func (c *promptCache) on(s Session, h Handle, now time.Time, read func() *Prompt) *Prompt {
	if s.Status != Waiting || s.Since.IsZero() {
		return read()
	}
	c.mu.Lock()
	p, ok := c.seen[s.Key()]
	c.mu.Unlock()
	if ok && p.since.Equal(s.Since) && p.why == s.WaitingFor && p.pane == h.ID && now.Sub(p.at) >= 0 && now.Sub(p.at) < promptRecheck {
		return p.prompt
	}
	got := read()
	c.mu.Lock()
	if c.seen == nil {
		c.seen = map[string]shownPrompt{}
	}
	for k, old := range c.seen {
		if now.Sub(old.at) > 10*promptRecheck {
			delete(c.seen, k)
		}
	}
	c.seen[s.Key()] = shownPrompt{since: s.Since, why: s.WaitingFor, pane: h.ID, at: now, prompt: got}
	c.mu.Unlock()
	return got
}
