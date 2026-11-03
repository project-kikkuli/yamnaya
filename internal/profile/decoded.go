package profile

import (
	"os"
	"sync"
	"time"
)

// settledAfter is how old a file must be before its decoding is kept: a rewrite inside one timestamp tick leaves no trace in its stat.
const settledAfter = 2 * time.Second

type decodedFile struct {
	info  os.FileInfo
	value any
	err   error
}

var (
	decodedMu    sync.Mutex
	decodedFiles = map[string]decodedFile{}
)

func unchanged[T any](path string, info os.FileInfo) (T, bool, error) {
	decodedMu.Lock()
	entry, ok := decodedFiles[path]
	decodedMu.Unlock()
	if ok && os.SameFile(entry.info, info) && entry.info.Size() == info.Size() && entry.info.ModTime().Equal(info.ModTime()) {
		if value, ok := entry.value.(T); ok {
			return value, true, entry.err
		}
	}
	var zero T
	return zero, false, nil
}

// readDecoded is decode of the file at path, decoded again only once the file's identity, size or modification time changes.
func readDecoded[T any](path string, decode func([]byte) (T, error)) (T, error) {
	var zero T
	info, err := os.Stat(path)
	if err != nil {
		return zero, err
	}
	if value, ok, err := unchanged[T](path, info); ok {
		return value, err
	}
	settled := time.Since(info.ModTime()) > settledAfter
	b, err := os.ReadFile(path)
	if err != nil {
		return zero, err
	}
	value, err := decode(b)
	if settled {
		decodedMu.Lock()
		decodedFiles[path] = decodedFile{info: info, value: value, err: err}
		decodedMu.Unlock()
	}
	return value, err
}
