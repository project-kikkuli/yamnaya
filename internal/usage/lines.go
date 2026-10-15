package usage

import (
	"bufio"
	"errors"
	"io"
	"os"
	"time"
)

// readLines calls fn for each complete line after offset and returns the offset past the last one.
func readLines(path string, offset int64, fn func(line []byte)) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return offset, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	r := bufio.NewReaderSize(f, 1<<18)
	var long []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, chunk...)
			continue
		}
		if err != nil {
			return offset, nil
		}
		line := chunk
		if long != nil {
			line = append(long, chunk...)
			long = nil
		}
		offset += int64(len(line))
		fn(line)
	}
}

// day is the local calendar date of t.
func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Local().Format(time.DateOnly)
}
