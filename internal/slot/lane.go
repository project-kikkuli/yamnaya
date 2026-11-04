package slot

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Lane serializes a named kind of work before it takes a machine slot. Waiters go oldest first, each by a ticket the
// kernel frees when it dies; the lane itself stays a flock, so jobs from a ym older than tickets still exclude and are
// excluded. seated says the holder has its slot, so others stop deferring to it; leave gives the lane back.
func (p Pool) Lane(ctx context.Context, name string, claim Claim, onWait func()) (leave, seated func(), err error) {
	if name == "" {
		return func() {}, func() {}, nil
	}
	key := laneKey(name)
	dir := filepath.Join(p.ticketDir(), key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(filepath.Join(p.Dir, "lane-"+key+".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, nil, err
	}
	claim.Lane = name
	t, err := takeTicket(dir, claim)
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	fail := func(err error) (func(), func(), error) {
		t.drop()
		f.Close()
		return nil, nil, err
	}
	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		line, err := liveTickets(dir, queuedTicket)
		if err != nil {
			return fail(err)
		}
		if len(line) > 0 && line[0] == t.path {
			err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				if err := t.hold(); err != nil {
					return fail(err)
				}
				return func() {
					t.drop()
					f.Close()
				}, t.drop, nil
			}
			if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
				return fail(err)
			}
		}
		if first && onWait != nil {
			onWait()
		}
		select {
		case <-ctx.Done():
		case <-time.After(p.Poll):
		}
	}
}

func laneKey(name string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(name))) }

// ownHolderTicket is the ticket of the job holding lane name, which a job calling this is: the lane's flock lets only one
// hold it. "" when it took no ticket.
func (p Pool) ownHolderTicket(name string) (string, error) {
	holders, err := liveTickets(filepath.Join(p.ticketDir(), laneKey(name)), holderTicket)
	if err != nil || len(holders) == 0 {
		return "", err
	}
	return holders[0], nil
}

// laneDirs are the directories of tickets, one for each lane that has been used.
func (p Pool) laneDirs() ([]string, error) {
	entries, err := os.ReadDir(p.ticketDir())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dirs := make([]string, 0, len(entries))
	for _, e := range entries {
		dirs = append(dirs, filepath.Join(p.ticketDir(), e.Name()))
	}
	return dirs, nil
}

// laneHolderWaits reports whether any lane's live holder is still waiting for a slot it could take.
func (p Pool) laneHolderWaits() (bool, error) {
	dirs, err := p.laneDirs()
	if err != nil {
		return false, err
	}
	for _, dir := range dirs {
		holders, err := liveTickets(dir, holderTicket)
		if err != nil {
			return false, err
		}
		for _, path := range holders {
			if !p.diskHolds(path) {
				return true, nil
			}
		}
	}
	return false, nil
}

// laneClaims are the claims of every live lane holder waiting for a slot and every lane waiter, with its place in line.
func (p Pool) laneClaims() ([]Claim, error) {
	dirs, err := p.laneDirs()
	if err != nil {
		return nil, err
	}
	var claims []Claim
	for _, dir := range dirs {
		holders, err := liveTickets(dir, holderTicket)
		if err != nil {
			return nil, err
		}
		line, err := liveTickets(dir, queuedTicket)
		if err != nil {
			return nil, err
		}
		for _, path := range holders {
			if c, err := readClaim(path); err == nil {
				c.LaneHolder = true
				claims = append(claims, c)
			}
		}
		for i, path := range line {
			if c, err := readClaim(path); err == nil {
				c.LanePlace = i + 1
				claims = append(claims, c)
			}
		}
	}
	return claims, nil
}
