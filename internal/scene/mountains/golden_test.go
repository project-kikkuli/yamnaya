package mountains

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// TestSamplesDrawTheirGoldenFrames holds the mountains to the frames and click targets in testdata/golden.
// YM_GOLDEN_UPDATE=1 rewrites them after a change meant to alter the picture.
func TestSamplesDrawTheirGoldenFrames(t *testing.T) {
	for _, s := range samples() {
		for _, frame := range []int{0, 17} {
			name := fmt.Sprintf("%s-f%d", s.name, frame)
			t.Run(name, func(t *testing.T) {
				got := goldenText(s, frame)
				path := filepath.Join("testdata", "golden", name+".ans.gz")
				if os.Getenv("YM_GOLDEN_UPDATE") != "" {
					var z bytes.Buffer
					w, _ := gzip.NewWriterLevel(&z, gzip.BestCompression)
					w.Write([]byte(got))
					w.Close()
					if err := os.WriteFile(path, z.Bytes(), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				f, err := os.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				defer f.Close()
				r, err := gzip.NewReader(f)
				if err != nil {
					t.Fatal(err)
				}
				want, err := io.ReadAll(r)
				if err != nil {
					t.Fatal(err)
				}
				if got == string(want) {
					return
				}
				g, w := strings.Split(got, "\n"), strings.Split(string(want), "\n")
				for i := range min(len(g), len(w)) {
					if g[i] != w[i] {
						t.Fatalf("line %d differs from the golden frame:\n got %q\nwant %q", i, g[i], w[i])
					}
				}
				t.Fatalf("%d lines, golden has %d", len(g), len(w))
			})
		}
	}
}

// goldenText is a sample's frame, and on frame 0 the peak or range every other cell of every third row hits.
func goldenText(s sample, frame int) string {
	var b strings.Builder
	b.WriteString(scene.Frame(s.scene, s.w, s.h, frame, s.table))
	if frame == 0 {
		b.WriteString("\n--hits--\n")
		for y := 0; y < s.h; y += 3 {
			for x := 0; x < s.w; x += 2 {
				fmt.Fprintf(&b, "%s,", scene.Hit(s.scene, s.w, s.h, s.table, x, y))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
