package nativefile

import (
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCompressedTranscriptsUseDecodedOffsetsAndSizes(t *testing.T) {
	text := "first record\nsecond record\nthird record\n"
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderConcurrency(1))
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	path := filepath.Join(t.TempDir(), "rollout.jsonl.zst")
	if err = os.WriteFile(path, encoder.EncodeAll([]byte(text), nil), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := Stat(path)
	if err != nil || info.Size() != int64(len(text)) {
		t.Fatalf("decoded stat %v %v", info, err)
	}
	reader, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	for _, start := range []int64{13, 0, 26, int64(len(text))} {
		if _, err = reader.Seek(start, io.SeekStart); err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(reader)
		if err != nil || string(data) != text[start:] {
			t.Fatalf("decoded seek %d: %q %v", start, data, err)
		}
	}
}
