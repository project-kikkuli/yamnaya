package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// serveLogCap is the size past which the serve log moves to work-service.log.1, replacing the one before, so the two stay bounded.
const serveLogCap = 1 << 20

// serveLog writes the serve log a line at a time, each prefixed with its RFC3339 time. It rotates by copying the file aside and
// truncating it in place, so the descriptors launchd or a starting ym gave serve for its output keep appending to the live file.
type serveLog struct {
	mu      sync.Mutex
	f       *os.File
	path    string
	cap     int64
	partial []byte
	now     func() time.Time
}

// openServeLog is the timestamped log for serve when its output already goes to path, as it does when launchd or a ym starts it, and
// nil otherwise, such as when someone runs ym serve in a terminal.
func openServeLog(stderr io.Writer, path string) *serveLog {
	out, ok := stderr.(*os.File)
	if !ok {
		return nil
	}
	have, err := out.Stat()
	if err != nil {
		return nil
	}
	if want, err := os.Stat(path); err != nil || !os.SameFile(have, want) {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil
	}
	return &serveLog{f: f, path: path, cap: serveLogCap, now: time.Now}
}

func (l *serveLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.partial = append(l.partial, p...)
	var out []byte
	for {
		i := bytes.IndexByte(l.partial, '\n')
		if i < 0 {
			break
		}
		out = append(out, l.now().Format(time.RFC3339)+" "...)
		out = append(out, l.partial[:i+1]...)
		l.partial = l.partial[i+1:]
	}
	if len(out) == 0 {
		return len(p), nil
	}
	if err := l.rotate(); err != nil {
		return 0, err
	}
	if _, err := l.f.Write(out); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (l *serveLog) rotate() error {
	info, err := l.f.Stat()
	if err != nil || info.Size() < l.cap {
		return err
	}
	old, err := os.ReadFile(l.path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(l.path+".1", old, 0600); err != nil {
		return err
	}
	return l.f.Truncate(0)
}

// Close writes out any unfinished last line.
func (l *serveLog) Close() error {
	if len(l.partial) > 0 {
		l.Write([]byte("\n"))
	}
	return l.f.Close()
}

// loggedError is an error serve has already written to its log, which ym's main does not print again.
type loggedError struct{ error }

func (e loggedError) Unwrap() error { return e.error }

// Logged reports whether err was already written to the serve log, or is a command's exit status ym slot passes on.
func Logged(err error) bool {
	var l loggedError
	var status exitStatus
	return errors.As(err, &l) || errors.As(err, &status)
}
