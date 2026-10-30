package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type byteCounter int64

func (n *byteCounter) Write(p []byte) (int, error) {
	*n += byteCounter(len(p))
	return len(p), nil
}

func BenchmarkKaijuBoard(b *testing.B) {
	old, colors := pal, scene.Colors
	pal, scene.Colors = newPalette(true), scene.TrueColor
	b.Cleanup(func() { pal, scene.Colors = old, colors })
	for _, size := range []struct {
		name        string
		w, h, units int
	}{{"small", 80, 24, 5}, {"large", 200, 60, 30}, {"wide", 260, 80, 30}} {
		for _, op := range []string{"animate", "key"} {
			b.Run(size.name+"/"+op, func(b *testing.B) {
				m := New(&core.Core{})
				m.loaded, m.width, m.height = true, size.w, size.h
				m.peaks.hidden, m.peaks.theme, m.peaks.dark = false, "kaiju", true
				m.board = core.Board{At: time.Now(), Fleet: core.Fleet{Ready: true}}
				for i := range size.units {
					p := missionBoard(m.board.At).Projects[i%3]
					p.Key, p.Name, p.Repo = fmt.Sprint("unit", i), fmt.Sprint("ship unit ", i), fmt.Sprint("repo", i/5)
					p.Sessions[0].ID = p.Key
					m.board.Projects = append(m.board.Projects, p)
				}
				m.board = measured(m.board)
				for i := 0; i < size.units; i += 5 {
					p := m.board.Projects[i]
					m.board.Repos = append(m.board.Repos, core.Repo{Key: p.Repo, Name: p.Repo, Missions: m.board.Projects[i:min(i+5, size.units)]})
				}
				if len(m.rows()) < 2 {
					b.Fatal("board has no selectable units")
				}
				m.out = &output{}
				page := m.prepareView()
				m.page = &page
				for {
					m.settle(true)
					if m.peaks.screen.left == 0 {
						break
					}
				}
				var bytes byteCounter
				renderer := uv.NewTerminalRenderer(&bytes, []string{"TERM=xterm-256color"})
				renderer.SetColorProfile(colorprofile.TrueColor)
				renderer.SetFullscreen(true)
				renderer.Resize(size.w, size.h)
				buf := uv.NewScreenBuffer(size.w, size.h)
				b.ReportAllocs()
				b.ResetTimer()
				for i := range b.N {
					if op == "key" {
						key := "j"
						if i%2 == 1 {
							key = "k"
						}
						next, _ := m.Update(keyOf(key))
						m = next.(Model)
					} else {
						m.peaks.frame = i + 1
						m.settle(true)
					}
					v := m.View()
					uv.NewStyledString(v.Content).Draw(buf, buf.Bounds())
					renderer.Render(buf.RenderBuffer)
					if err := renderer.Flush(); err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(bytes)/float64(b.N), "ANSI-B/op")
			})
		}
	}
}
