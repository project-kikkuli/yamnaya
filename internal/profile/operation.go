package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ErrOperationBusy is the transient refusal of Operation while another holds the conversation.
var ErrOperationBusy = errors.New("another session operation owns this conversation")

func Operation(dir, harness, session string) (func(), error) {
	sum := sha256.Sum256([]byte(harness + "/" + session))
	path := filepath.Join(dir, "session-operations", hex.EncodeToString(sum[:])+".lock")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("%w: %w", ErrOperationBusy, err)
	}
	return func() { f.Close() }, nil
}
