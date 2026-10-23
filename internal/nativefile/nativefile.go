package nativefile

import (
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

type Reader interface {
	io.Reader
	io.Seeker
	io.Closer
	Stat() (os.FileInfo, error)
}
type compressed struct {
	file    *os.File
	decoder *zstd.Decoder
	offset  int64
}

func Open(path string) (Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".zst") {
		return f, nil
	}
	decoder, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1))
	if err != nil {
		f.Close()
		return nil, err
	}
	return &compressed{file: f, decoder: decoder}, nil
}
func (r *compressed) Read(b []byte) (int, error) {
	n, err := r.decoder.Read(b)
	r.offset += int64(n)
	return n, err
}
func (r *compressed) Close() error { r.decoder.Close(); return r.file.Close() }
func (r *compressed) Seek(offset int64, whence int) (int64, error) {
	if whence != io.SeekStart || offset < 0 {
		return r.offset, os.ErrInvalid
	}
	if offset < r.offset {
		if _, err := r.file.Seek(0, io.SeekStart); err != nil {
			return r.offset, err
		}
		if err := r.decoder.Reset(r.file); err != nil {
			return r.offset, err
		}
		r.offset = 0
	}
	_, err := io.CopyN(io.Discard, r, offset-r.offset)
	return r.offset, err
}
func (r *compressed) Stat() (os.FileInfo, error) { return Stat(r.file.Name()) }

type fileInfo struct {
	os.FileInfo
	size int64
}

func (f fileInfo) Size() int64 { return f.size }

type sizeEntry struct {
	size, compressed int64
	mtime            time.Time
}

var sizeCache = struct {
	sync.Mutex
	entries map[string]sizeEntry
}{entries: map[string]sizeEntry{}}

func Stat(path string) (os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil || !strings.HasSuffix(path, ".zst") {
		return info, err
	}
	sizeCache.Lock()
	entry, ok := sizeCache.entries[path]
	sizeCache.Unlock()
	if ok && entry.compressed == info.Size() && entry.mtime.Equal(info.ModTime()) {
		return fileInfo{info, entry.size}, nil
	}
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	size, err := io.Copy(io.Discard, r)
	if err != nil {
		return nil, err
	}
	sizeCache.Lock()
	sizeCache.entries[path] = sizeEntry{size: size, compressed: info.Size(), mtime: info.ModTime()}
	sizeCache.Unlock()
	return fileInfo{info, size}, nil
}
func ReadFile(path string) ([]byte, error) {
	r, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}
func IsTranscript(path string) bool {
	return strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".jsonl.zst")
}
func PlainName(path string) string { return strings.TrimSuffix(path, ".zst") }
