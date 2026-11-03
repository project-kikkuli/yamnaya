package cli

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// boardFrame starts what ym board --since prints: the frame's kind and the snapshot's token on one line, then its gzipped body.
const boardFrame = "ym-board1 "

const (
	frameSame  = "same"
	frameFull  = "full"
	frameDelta = "delta"
	// noBoard is what a reader holding no snapshot passes to --since.
	noBoard = "-"
	// boardBases is how many snapshots a host keeps to answer readers a few snapshots behind.
	boardBases = 8
	deltaBlock = 32
)

func boardToken(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// writeBoardSince prints raw for a reader holding the snapshot named since: nothing when it holds raw, else what it lacks.
func writeBoardSince(w io.Writer, dir string, raw []byte, since string) error {
	token := boardToken(raw)
	if since == token {
		_, err := fmt.Fprintf(w, "%s%s %s\n", boardFrame, frameSame, token)
		return err
	}
	bases := filepath.Join(dir, "board-bases")
	if err := keepBoardBase(bases, token, raw); err != nil {
		return err
	}
	kind, body := frameFull, raw
	if base, err := os.ReadFile(filepath.Join(bases, filepath.Base(since))); err == nil {
		kind, body = frameDelta, boardDelta(base, raw)
	}
	if _, err := fmt.Fprintf(w, "%s%s %s\n", boardFrame, kind, token); err != nil {
		return err
	}
	gz := gzip.NewWriter(w)
	if _, err := gz.Write(body); err != nil {
		return err
	}
	return gz.Close()
}

// keepBoardBase stores raw under token, replacing a damaged copy, and removes all but the newest boardBases snapshots.
func keepBoardBase(bases, token string, raw []byte) error {
	if err := os.MkdirAll(bases, 0o700); err != nil {
		return err
	}
	path := filepath.Join(bases, token)
	f, err := os.CreateTemp(bases, ".new-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(raw)
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	entries, err := os.ReadDir(bases)
	if err != nil {
		return err
	}
	type kept struct {
		name string
		at   time.Time
	}
	var all []kept
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !strings.HasPrefix(e.Name(), ".") {
			all = append(all, kept{e.Name(), info.ModTime()})
		}
	}
	slices.SortFunc(all, func(a, b kept) int { return b.at.Compare(a.at) })
	for _, k := range all[min(boardBases, len(all)):] {
		os.Remove(filepath.Join(bases, k.name))
	}
	return nil
}

// boardDelta is target as runs of new bytes and ranges copied from base: repeated literal length, literal, copy offset, copy length.
func boardDelta(base, target []byte) []byte {
	const prime = 16777619
	var out, num []byte
	put := func(n int) {
		num = binary.AppendUvarint(num[:0], uint64(n))
		out = append(out, num...)
	}
	if len(base) < deltaBlock || len(target) < deltaBlock {
		put(len(target))
		return append(out, target...)
	}
	table := make([]int32, 1<<18)
	slot := func(h uint32) uint32 { return (h * 2654435761) >> 14 }
	hash := func(b []byte) uint32 {
		var h uint32
		for _, c := range b[:deltaBlock] {
			h = h*prime + uint32(c)
		}
		return h
	}
	for i := 0; i+deltaBlock <= len(base); i += deltaBlock {
		table[slot(hash(base[i:]))] = int32(i + 1)
	}
	var drop uint32 = 1
	for range deltaBlock {
		drop *= prime
	}
	lit, i := 0, 0
	h := hash(target)
	for i+deltaBlock <= len(target) {
		if p := int(table[slot(h)]) - 1; p >= 0 && bytes.Equal(base[p:p+deltaBlock], target[i:i+deltaBlock]) {
			start, end, stop := p, p+deltaBlock, i+deltaBlock
			for end < len(base) && stop < len(target) && base[end] == target[stop] {
				end, stop = end+1, stop+1
			}
			at := i
			for start > 0 && at > lit && base[start-1] == target[at-1] {
				start, at = start-1, at-1
			}
			put(at - lit)
			out = append(out, target[lit:at]...)
			put(start)
			put(end - start)
			lit, i = stop, stop
			if i+deltaBlock <= len(target) {
				h = hash(target[i:])
			}
			continue
		}
		if i+deltaBlock < len(target) {
			h = h*prime + uint32(target[i+deltaBlock]) - uint32(target[i])*drop
		}
		i++
	}
	put(len(target) - lit)
	return append(out, target[lit:]...)
}

// applyBoardDelta rebuilds the target boardDelta encoded against base.
func applyBoardDelta(base, delta []byte) ([]byte, error) {
	broken := errors.New("broken board delta")
	next := func() (int, bool) {
		n, size := binary.Uvarint(delta)
		if size <= 0 || n > uint64(len(base))+uint64(len(delta)) {
			return 0, false
		}
		delta = delta[size:]
		return int(n), true
	}
	var out []byte
	for {
		lit, ok := next()
		if !ok || lit > len(delta) {
			return nil, broken
		}
		out = append(out, delta[:lit]...)
		delta = delta[lit:]
		if len(delta) == 0 {
			return out, nil
		}
		off, ok := next()
		if !ok {
			return nil, broken
		}
		n, ok := next()
		if !ok || off+n > len(base) {
			return nil, broken
		}
		out = append(out, base[off:off+n]...)
	}
}

// readBoardFrame is the snapshot a frame carries given the one the reader held, and its token.
func readBoardFrame(frame, held []byte) (raw []byte, token, kind string, err error) {
	line, body, ok := bytes.Cut(frame, []byte("\n"))
	fields := strings.Fields(strings.TrimPrefix(string(line), boardFrame))
	if !ok || len(fields) != 2 {
		return nil, "", "", fmt.Errorf("ym board printed a broken frame: %.40q", line)
	}
	kind, token = fields[0], fields[1]
	if kind == frameSame {
		return held, token, kind, nil
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		return nil, "", "", fmt.Errorf("ym board frame: %w", err)
	}
	raw, err = io.ReadAll(gz)
	if err != nil {
		return nil, "", "", fmt.Errorf("ym board frame: %w", err)
	}
	if kind == frameDelta {
		if raw, err = applyBoardDelta(held, raw); err != nil {
			return nil, "", "", err
		}
	}
	if got := boardToken(raw); got != token {
		return nil, "", "", fmt.Errorf("ym board frame rebuilt %s, want %s", got, token)
	}
	return raw, token, kind, nil
}
