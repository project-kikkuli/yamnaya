package themes

import (
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func BenchmarkKaiju(b *testing.B) {
	for _, size := range []struct {
		name               string
		w, h, groups, rows int
	}{{"small", 80, 24, 1, 6}, {"large", 200, 60, 6, 20}, {"wide", 260, 80, 6, 20}} {
		b.Run(size.name, func(b *testing.B) {
			s := busy("kaiju", 0)
			s.Ranges = s.Ranges[:size.groups]
			s.Now = time.Date(2026, 10, 30, 20, 0, 0, 0, time.UTC)
			text := table(size.rows)
			box := scene.TableBox(text, size.w, size.h)
			th := scene.Lookup(s.Theme)
			for _, op := range []string{"paint", "frame", "hit", "cold"} {
				b.Run(op, func(b *testing.B) {
					th.Paint(s, size.w, size.h, 0, box)
					b.ReportAllocs()
					b.ResetTimer()
					for i := range b.N {
						switch op {
						case "paint":
							th.Paint(s, size.w, size.h, i+1, box)
						case "frame":
							scene.Frame(s, size.w, size.h, i+1, text)
						case "hit":
							th.Hit(s, size.w, size.h, box, i%size.w, i/size.w%size.h)
						case "cold":
							s.Dark = !s.Dark
							th.Paint(s, size.w, size.h, i+1, box)
						}
					}
				})
			}
		})
	}
}
