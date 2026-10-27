package themes

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// TestPreview writes each theme's busy board, mid-kill, as a .ans file to cat in a terminal, when
// YM_THEME_PREVIEW names a directory.
func TestPreview(t *testing.T) {
	dir := os.Getenv("YM_THEME_PREVIEW")
	if dir == "" {
		t.Skip("set YM_THEME_PREVIEW to a directory to write previews")
	}
	for _, th := range scene.Themes() {
		for _, frame := range []int{0, 4, 8} {
			out := scene.Frame(busy(th.Name(), float64(frame)/8), 200, 60, frame, table(12))
			if err := os.WriteFile(filepath.Join(dir, th.Name()+"-"+string(rune('0'+frame))+".ans"), []byte(out+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
