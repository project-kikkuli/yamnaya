package usage

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestAnEntryMadeInTheTickOfTheLastListingIsListed(t *testing.T) {
	for _, age := range []time.Duration{0, time.Second} {
		t.Run(age.String(), func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			tick := time.Now().Add(-age).Truncate(time.Millisecond)
			if err := os.Chtimes(dir, tick, tick); err != nil {
				t.Fatal(err)
			}
			readDir(dir)
			if err := os.WriteFile(filepath.Join(dir, "b.jsonl"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(dir, tick, tick); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range readDir(dir) {
				names = append(names, e.Name())
			}
			if !slices.Contains(names, "b.jsonl") {
				t.Fatalf("listing %q lost the entry made in its directory's last tick", names)
			}
		})
	}
}
