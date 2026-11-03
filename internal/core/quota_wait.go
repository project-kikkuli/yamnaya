package core

import (
	"errors"
	"fmt"
	"time"
)

// QuotaRecheck is how often ym asks again whether a session refused for an exhausted login can come back now.
const QuotaRecheck = time.Minute

// QuotaWait is a resume ym refused because the session's login is out of its subscription limit and no other login is usable.
type QuotaWait struct {
	Harness, Login, Key string
	Until               time.Time
}

func (q *QuotaWait) Error() string {
	return fmt.Sprintf("%s login %s is out of its subscription limit until %s and no other usable login of any harness is configured; ym resumes it after that. ym switch %s --profile <id> moves it now, ym revive %s --anyway resumes it if the limit reset early",
		q.Harness, q.Login, q.Until.UTC().Format(time.RFC3339), q.Key, q.Key)
}

// retryQuota makes a refusal for an exhausted login cost neither the resume pace nor the session's one try: it is asked again after QuotaRecheck.
func (c *Core) retryQuota(session string, err error) {
	var wait *QuotaWait
	if !errors.As(err, &wait) {
		return
	}
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = r.before
	if r.recheck == nil {
		r.recheck = map[string]time.Time{}
	}
	r.recheck[session] = c.now().Add(QuotaRecheck)
}

// CanResume reports whether the resume pace allows bringing a session back now.
func (c *Core) CanResume() bool {
	now := c.now()
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last.IsZero() || now.Before(r.last) || now.Sub(r.last) >= ResumePace
}

// ResumedNow counts a resume made outside ReviveWork against the resume pace.
func (c *Core) ResumedNow() {
	now := c.now()
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	r.before, r.last = r.last, now
}
