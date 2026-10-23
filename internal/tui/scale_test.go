package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scale"
)

// screen is a terminal that keeps the last frame and counts the bytes a renderer that rewrites only changed lines sends it.
type screen struct {
	last  []string
	bytes int
}

func (s *screen) show(frame string) {
	lines := strings.Split(frame, "\n")
	for i, l := range lines {
		if i >= len(s.last) || s.last[i] != l {
			s.bytes += len(l) + 1
		}
	}
	s.last = lines
}

// thousand is the board over a thousand goals on a 200 by 60 color terminal, as the owner sees it.
func thousand(b *testing.B) (*scale.Rig, Model) {
	old := pal
	pal = newPalette(true)
	b.Cleanup(func() { pal = old })
	rig := scale.Open(b, scale.Thousand)
	board, err := rig.Core.Board()
	if err != nil {
		b.Fatal(err)
	}
	m := New(rig.Core)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	next, _ = next.(Model).Update(boardMsg{board: board})
	return rig, next.(Model)
}

// BenchmarkBoardReload is the board taking one reload over a thousand goals and drawing it, with the bytes it sends per second.
func BenchmarkBoardReload(b *testing.B) {
	for _, byRepo := range []bool{false, true} {
		name := "tree"
		if byRepo {
			name = "by-repo"
		}
		b.Run(name, func(b *testing.B) {
			rig, m := thousand(b)
			m.byRepo = byRepo
			boards := make([]core.Board, 0, 8)
			for range cap(boards) {
				rig.Advance(time.Second)
				board, err := rig.Core.Board()
				if err != nil {
					b.Fatal(err)
				}
				boards = append(boards, board)
			}
			var term screen
			term.show(m.View().Content)
			term.bytes = 0
			i := 0
			scale.Measure(b, func() {
				next, _ := m.Update(boardMsg{board: boards[i%len(boards)]})
				m = next.(Model)
				term.show(m.View().Content)
				i++
			})
			b.ReportMetric(float64(term.bytes)/float64(b.N), "term-bytes/s")
		})
	}
}

// BenchmarkMountainFrame is one animation frame of the mountains over a thousand goals, with the bytes it sends.
func BenchmarkMountainFrame(b *testing.B) {
	_, m := thousand(b)
	var term screen
	term.show(m.View().Content)
	term.bytes = 0
	frame := 0
	scale.Measure(b, func() {
		frame++
		m.peaks.frame = frame
		term.show(m.View().Content)
	})
	b.ReportMetric(float64(term.bytes)/float64(b.N), "term-bytes/frame")
}
