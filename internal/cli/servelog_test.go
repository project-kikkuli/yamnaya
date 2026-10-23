package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServeLogStampsEveryLineAndStaysBounded(t *testing.T) {
	at := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name   string
		writes []string
		cap    int64
		want   string
		old    string
	}{
		{"one line", []string{"cycle failed\n"}, 1 << 20, "2026-10-23T12:00:00Z cycle failed\n", ""},
		{"joined error over lines", []string{"a\nb\n"}, 1 << 20, "2026-10-23T12:00:00Z a\n2026-10-23T12:00:00Z b\n", ""},
		{"line split across writes", []string{"half ", "whole\n"}, 1 << 20, "2026-10-23T12:00:00Z half whole\n", ""},
		{"past the cap", []string{"first\n", "second\n"}, 10, "2026-10-23T12:00:00Z second\n", "2026-10-23T12:00:00Z first\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "work-service.log")
			out, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer out.Close()
			l := openServeLog(out, path)
			if l == nil {
				t.Fatal("serve writing to its log got no timestamped log")
			}
			l.cap, l.now = c.cap, func() time.Time { return at }
			for _, w := range c.writes {
				l.Write([]byte(w))
			}
			l.Close()
			if got, _ := os.ReadFile(path); string(got) != c.want {
				t.Fatalf("log = %q, want %q", got, c.want)
			}
			if got, _ := os.ReadFile(path + ".1"); string(got) != c.old {
				t.Fatalf("rotated log = %q, want %q", got, c.old)
			}
		})
	}
}

func TestServeInATerminalWritesPlainOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-service.log")
	os.WriteFile(path, nil, 0600)
	other, err := os.Create(filepath.Join(t.TempDir(), "elsewhere"))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for name, w := range map[string]io.Writer{"another file": other, "not a file": &bytes.Buffer{}} {
		if openServeLog(w, path) != nil {
			t.Fatalf("%s: serve got a timestamped log though its output goes elsewhere", name)
		}
	}
}
