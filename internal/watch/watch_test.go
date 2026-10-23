package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestEveryChangeShowsOnTheNextStat changes a file or its directory each way an agent's files change and expects the very
// next Stat to see it, with nothing in between.
func TestEveryChangeShowsOnTheNextStat(t *testing.T) {
	for _, c := range []struct {
		name   string
		exists bool
		change func(t *testing.T, path string)
		want   func(os.FileInfo, error) bool
	}{
		{"append", true, func(t *testing.T, p string) { appendTo(t, p, "more\n") }, size(11)},
		{"rewrite in place", true, func(t *testing.T, p string) { must(t, os.WriteFile(p, []byte("x"), 0o644)) }, size(1)},
		{"replace by rename", true, func(t *testing.T, p string) {
			must(t, os.WriteFile(p+".new", []byte("renamed!!"), 0o644))
			must(t, os.Rename(p+".new", p))
		}, size(9)},
		{"times only", true, func(t *testing.T, p string) {
			old := time.Now().Add(-time.Hour)
			must(t, os.Chtimes(p, old, old))
		}, func(i os.FileInfo, err error) bool { return err == nil && time.Since(i.ModTime()) > 30*time.Minute }},
		{"delete", true, func(t *testing.T, p string) { must(t, os.Remove(p)) }, func(_ os.FileInfo, err error) bool { return err != nil }},
		{"appear", false, func(t *testing.T, p string) { must(t, os.WriteFile(p, []byte("new"), 0o644)) }, size(3)},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "a.jsonl")
			sibling := filepath.Join(dir, "b.jsonl")
			if c.exists {
				must(t, os.WriteFile(path, []byte("first\n"), 0o644))
			}
			s := New()
			for range 3 {
				s.Refresh()
				s.Stat(path)
				s.Stat(sibling)
			}
			c.change(t, path)
			s.Refresh()
			if info, err := s.Stat(path); !c.want(info, err) {
				t.Fatalf("after the change Stat says %v, %v", info, err)
			}
			if _, err := s.Stat(sibling); err == nil {
				t.Fatal("a missing sibling shows up")
			}
		})
	}
}

func size(n int64) func(os.FileInfo, error) bool {
	return func(i os.FileInfo, err error) bool { return err == nil && i.Size() == n }
}

func appendTo(t *testing.T, path, text string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	must(t, err)
	defer f.Close()
	_, err = f.WriteString(text)
	must(t, err)
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// BenchmarkStat is one look at an unchanged file, through the watch and straight.
func BenchmarkStat(b *testing.B) {
	path := filepath.Join(b.TempDir(), "a")
	must(&testing.T{}, os.WriteFile(path, []byte("x"), 0o644))
	s := New()
	s.Stat(path)
	b.Run("watched", func(b *testing.B) {
		for b.Loop() {
			s.Stat(path)
		}
	})
	b.Run("stat", func(b *testing.B) {
		for b.Loop() {
			os.Stat(path)
		}
	})
}
