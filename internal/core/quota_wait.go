package core

import (
	"errors"
	"fmt"
	"time"
)

// QuotaRecheck is how often ym asks again whether a session refused for an exhausted login can come back now.
const QuotaRecheck = time.Minute

// ResumeRetry is how long ym waits before trying again a resume that failed for any other reason.
const ResumeRetry = 5 * time.Minute

// QuotaWait is a resume ym refused because the session's login is out of its subscription limit and no other login is usable.
type QuotaWait struct {
	Harness, Login, Key string
	Until               time.Time
	// Held is why it cannot move to the other harness, when something holds that.
	Held string
}

func (q *QuotaWait) Error() string {
	if q.Held != "" {
		return fmt.Sprintf("%s login %s is out of its subscription limit until %s and it cannot move yet: %s",
			q.Harness, q.Login, q.Until.UTC().Format(time.RFC3339), q.Held)
	}
	return fmt.Sprintf("%s login %s is out of its subscription limit until %s and no other login ym can verify as usable; ym resumes it after that. ym switch %s --profile <id> moves it now; once it is stopped, ym revive %s --anyway resumes it if the limit reset early",
		q.Harness, q.Login, q.Until.UTC().Format(time.RFC3339), q.Key, q.Key)
}

// resumeFailedKey makes one event of a refusal per login and window, however a second or so of a provider's jitter moves the reset, and
// one per distinct error otherwise.
func resumeFailedKey(session string, err error) string {
	var wait *QuotaWait
	if errors.As(err, &wait) {
		return "resume-failed\x00" + session + "\x00quota\x00" + wait.Harness + "/" + wait.Login + "\x00" + wait.Until.Truncate(time.Hour).UTC().Format(time.RFC3339)
	}
	return "resume-failed\x00" + session + "\x00" + err.Error()
}

// retryResume asks again after a resume that failed, as whatever stopped it may clear: a login being fixed, a limit resetting. A failure
// started nothing, so it does not cost the resume pace, and sessions that keep failing never starve one that would come back.
func (c *Core) retryResume(session string, err error) {
	if err == nil {
		return
	}
	var wait *QuotaWait
	quota := errors.As(err, &wait)
	r := &c.recovery
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recheck == nil {
		r.recheck = map[string]time.Time{}
	}
	r.last = r.before
	if quota {
		r.recheck[session] = c.now().Add(QuotaRecheck)
		return
	}
	r.recheck[session] = c.now().Add(ResumeRetry)
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
