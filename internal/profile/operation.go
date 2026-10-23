package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

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
		return nil, fmt.Errorf("another session operation owns this conversation: %w", err)
	}
	return func() { f.Close() }, nil
}
