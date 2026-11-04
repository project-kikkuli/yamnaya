package cli

import (
	"errors"
	"io/fs"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/launch"
)

// unreadableEvery is how often exited sessions' conversations are looked at: the board asks on every read and must not wait on the disk.
const unreadableEvery = time.Minute

// unreadableConversations remembers which exited sessions' conversations cannot be opened, for their rows. It looks in the background.
type unreadableConversations struct {
	mu      sync.Mutex
	why     map[string]string
	checked time.Time
	running bool
}

var unreadable unreadableConversations

func conversationKey(rec launch.Record) string { return rec.ConfigRoot + "\x00" + rec.Session }

func (u *unreadableConversations) of(rec launch.Record) string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.why[conversationKey(rec)]
}

func (u *unreadableConversations) refresh(recs []launch.Record) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.running || time.Since(u.checked) < unreadableEvery {
		return
	}
	u.running = true
	go func() {
		why := map[string]string{}
		for _, rec := range recs {
			if problem := conversationProblem(rec); problem != "" {
				why[conversationKey(rec)] = problem
			}
		}
		u.mu.Lock()
		defer u.mu.Unlock()
		u.why, u.checked, u.running = why, time.Now(), false
	}()
}

// conversationProblem is why ym cannot tell what became of rec's conversation, empty when it can.
func conversationProblem(rec launch.Record) string {
	_, err := conversation(rec)
	if err == nil {
		return ""
	}
	var open *fs.PathError
	if errors.As(err, &open) {
		err = open.Err
	}
	return "cannot read its conversation: " + err.Error()
}
