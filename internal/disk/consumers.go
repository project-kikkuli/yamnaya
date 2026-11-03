package disk

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Consumer is a directory and the bytes it occupies, as far as Measure counted.
type Consumer struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// String is the consumer in one short phrase.
func (c Consumer) String() string { return fmt.Sprintf("%s %s", c.Path, Bytes(c.Size)) }

// Bytes is n in the unit people read disks in.
func Bytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MiB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KiB", n>>10)
}

// Measure adds up the bytes on the filesystem dev under each root, grouped by the first two path levels below the root, and
// returns the largest top. It stops at budget files or when ctx ends, so one measurement stays bounded; complete says it finished.
func Measure(ctx context.Context, roots []string, dev uint64, budget, top int) (out []Consumer, complete bool) {
	sizes := map[string]int64{}
	seen := 0
	complete = true
	done := map[string]bool{}
	for _, root := range roots {
		root = filepath.Clean(root)
		if done[root] || underAny(root, roots) {
			continue
		}
		done[root] = true
		filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if seen++; seen > budget || ctx.Err() != nil {
				complete = false
				return filepath.SkipAll
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return nil
			}
			if uint64(st.Dev) != dev {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.IsDir() {
				sizes[group(root, path)] += int64(st.Blocks) * 512
			}
			return nil
		})
	}
	for path, size := range sizes {
		out = append(out, Consumer{path, size})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out[:min(top, len(out))], complete
}

func underAny(root string, roots []string) bool {
	for _, r := range roots {
		r = filepath.Clean(r)
		if r != root && strings.HasPrefix(root, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// group is the first two path levels of path below root.
func group(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return root
	}
	parts := strings.Split(rel, string(os.PathSeparator))
	if len(parts) <= 1 {
		return root
	}
	return filepath.Join(root, parts[0], parts[1])
}
