package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// binaryWatch notices when the file at path stops being the one that was there at startup.
type binaryWatch struct {
	path  string
	start os.FileInfo
	last  os.FileInfo
}

func watchBinary(path string) (*binaryWatch, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	return &binaryWatch{path: path, start: fi}, nil
}

// changed reports a new file once two polls in a row see the same one, so a binary still being written is not run.
func (w *binaryWatch) changed() bool {
	fi, err := os.Stat(w.path)
	if err != nil || sameFile(fi, w.start) {
		w.last = nil
		return false
	}
	settled := w.last != nil && sameFile(fi, w.last)
	w.last = fi
	return settled
}

func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// StaleWarning says, when the first ym on PATH is another file than self, written after started (self as it was when this ym
// started), that this ym is older than the one a shell runs; "" otherwise.
func StaleWarning(self string, started os.FileInfo) string {
	if started == nil {
		return ""
	}
	onPath, err := exec.LookPath("ym")
	if err != nil {
		return ""
	}
	newest, err := filepath.EvalSymlinks(onPath)
	if err != nil {
		return ""
	}
	if running, err := filepath.EvalSymlinks(self); err == nil && running == newest {
		return ""
	}
	fi, err := os.Stat(newest)
	if err != nil || os.SameFile(fi, started) || !fi.ModTime().After(started.ModTime()) {
		return ""
	}
	return fmt.Sprintf("this ym runs %s, older than %s first on PATH (written %s); run ym update, or restart it from PATH", self, onPath, fi.ModTime().Format("2006-01-02 15:04"))
}
