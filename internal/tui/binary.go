package tui

import "os"

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
