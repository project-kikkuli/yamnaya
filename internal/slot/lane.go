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

// Lane serializes a named kind of work before it takes a machine slot.
func (p Pool) Lane(ctx context.Context, name string, claim Claim, onWait func()) (func(), error) {
	if name == "" {
		return func() {}, nil
	}
	if err := os.MkdirAll(p.waitDir(), 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(p.Dir, fmt.Sprintf("lane-%x.lock", sha256.Sum256([]byte(name))))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	waiting := filepath.Join(p.waitDir(), fmt.Sprintf("%d.json", claim.PID))
	defer os.Remove(waiting)
	for first := true; ; first = false {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { f.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		if first {
			claim.Lane = name
			if err := writeJSON(waiting, claim); err != nil {
				f.Close()
				return nil, err
			}
			if onWait != nil {
				onWait()
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(p.Poll):
		}
	}
}
