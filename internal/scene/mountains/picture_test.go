package mountains

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type shot struct {
	r      rune
	fg, bg color.RGBA
	plain  bool
}

// cells decodes Frame output back into what a truecolor terminal would show in each cell.
func cells(t *testing.T, out string) [][]shot {
	t.Helper()
	var grid [][]shot
	for _, line := range strings.Split(out, "\n") {
		var row []shot
		fg, bg, plain := color.RGBA{}, color.RGBA{}, true
		for i := 0; i < len(line); {
			if line[i] == 0x1b {
				end := strings.IndexByte(line[i:], 'm')
				parts := strings.Split(line[i+2:i+end], ";")
				for k := 0; k < len(parts); k++ {
					switch parts[k] {
					case "0", "":
						plain = true
					case "38", "48":
						if parts[k+1] != "2" {
							k += 2
							continue
						}
						c := color.RGBA{atoi(parts[k+2]), atoi(parts[k+3]), atoi(parts[k+4]), 255}
						if parts[k] == "38" {
							fg = c
						} else {
							bg = c
						}
						plain = false
						k += 4
					}
				}
				i += end + 1
				continue
			}
			r, size := []rune(line[i:])[0], len(string([]rune(line[i:])[0]))
			row = append(row, shot{r, fg, bg, plain})
			i += size
		}
		grid = append(grid, row)
	}
	return grid
}

func atoi(s string) uint8 {
	v, _ := strconv.Atoi(s)
	return uint8(v)
}

func picture(t *testing.T, out string, dark bool, path string) {
	t.Helper()
	grid := cells(t, out)
	const cw, ch = 8, 16
	img := image.NewRGBA(image.Rect(0, 0, len(grid[0])*cw, len(grid)*ch))
	paper, ink := color.RGBA{250, 250, 250, 255}, color.RGBA{30, 30, 30, 255}
	if dark {
		paper, ink = color.RGBA{14, 16, 20, 255}, color.RGBA{215, 218, 222, 255}
	}
	for y, row := range grid {
		for x, c := range row {
			bg, fg := c.bg, c.fg
			if c.plain {
				bg, fg = paper, ink
			}
			for py := range ch {
				for px := range cw {
					col := bg
					switch {
					case c.r == '▀' && py < ch/2, c.r == '█', c.r == '▄' && py >= ch/2:
						col = fg
					case c.r != ' ' && c.r != '▀' && c.r != '▄' && px >= 2 && px < 6 && py >= 5 && py < 13:
						col = fg
					}
					img.Set(x*cw+px, y*ch+py, col)
				}
			}
		}
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

// TestPictures renders sample frames to PNG when YM_SCENE_PNG names a directory, for looking at by eye.
func TestPictures(t *testing.T) {
	dir := os.Getenv("YM_SCENE_PNG")
	if dir == "" {
		t.Skip("set YM_SCENE_PNG to a directory to render pictures")
	}
	scene.Colors = scene.TrueColor
	for _, c := range samples() {
		out := scene.Frame(c.scene, c.w, c.h, c.frame, c.table)
		picture(t, out, c.scene.Dark, filepath.Join(dir, c.name+".png"))
		if err := os.WriteFile(filepath.Join(dir, c.name+".ans"), []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
