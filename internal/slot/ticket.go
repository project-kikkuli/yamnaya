package slot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

const (
	queuedTicket = ".queued"
	holderTicket = ".holder"
)

// ticket is a place in a line, a claim in a file its owner keeps flock'd, so a ticket is live exactly while its owner runs, even past
// SIGKILL or a reused pid. Its name sorts by arrival and is never reused, so anyone may remove a dead one.
type ticket struct {
	f    *os.File
	path string
}

// takeTicket joins the line in dir. The ticket is locked and written before its name appears, so no reader finds it dead.
func takeTicket(dir string, c Claim) (*ticket, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(dir, ".new-*")
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("%020d-%d%s", time.Now().UnixNano(), c.PID, queuedTicket))
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		os.Remove(f.Name())
		f.Close()
		return nil, err
	}
	return &ticket{f: f, path: path}, nil
}

// hold marks the ticket's owner as its lane's holder waiting for a slot.
func (t *ticket) hold() error {
	path := strings.TrimSuffix(t.path, queuedTicket) + holderTicket
	if err := os.Rename(t.path, path); err != nil {
		return err
	}
	t.path = path
	return nil
}

func (t *ticket) drop() {
	os.Remove(t.path)
	t.f.Close()
}

// liveTickets are the tickets in dir with the given suffix whose owners live, oldest first; it removes dead ones.
func liveTickets(dir, suffix string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var live []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		held, err := ticketHeld(path)
		if err != nil {
			return nil, err
		}
		if held {
			live = append(live, path)
		} else {
			os.Remove(path)
		}
	}
	sort.Strings(live)
	return live, nil
}

func ticketHeld(path string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB) != nil, nil
}
