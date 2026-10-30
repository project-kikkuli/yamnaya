package kaiju

import (
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/agenttest"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestMain(m *testing.M) { os.Exit(agenttest.Run(m)) }

func TestWeatherFollowsTheCurrentSnapshot(t *testing.T) {
	old := scene.Colors
	scene.Colors = scene.TrueColor
	t.Cleanup(func() { scene.Colors = old })
	for _, weather := range []scene.Weather{scene.Clouds, scene.Storm, scene.Eruption} {
		for _, change := range []string{"progress", "resize", "weather", "selection", "remove", "daylight", "nested", "kill", "reappearance"} {
			t.Run(strconv.Itoa(int(weather))+"/"+change, func(t *testing.T) {
				s := lone(func(p *scene.Peak) { p.Weather = weather })
				w, h := 160, 48
				Kaiju{}.Paint(s, w, h, 5, scene.Rect{})
				switch change {
				case "progress":
					s.Ranges[0].Peaks[1].Progress = .85
				case "resize":
					w, h = 80, 24
				case "weather":
					s.Ranges[0].Peaks[1].Weather = scene.Weather((int(weather) + 1) % 5)
				case "selection":
					s.Selected = "u"
				case "remove":
					s.Ranges[0].Peaks = s.Ranges[0].Peaks[:1]
				case "daylight":
					s.Now = time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
				case "nested":
					s.Ranges = []scene.Range{{ID: "chief", Name: "chief", Ranges: s.Ranges}}
				case "kill":
					s.Ranges[0].Peaks[1].Dying = .5
				case "reappearance":
					s.Ranges[0].Peaks[1].Dying = .5
					Kaiju{}.Paint(s, w, h, 7, scene.Rect{})
					s.Ranges[0].Peaks[1].Dying = 0
				}
				warm := Kaiju{}.Paint(s, w, h, 9, scene.Rect{}).Draw("")
				backdrops.Lock()
				backdrops.b = nil
				backdrops.Unlock()
				if cold := (Kaiju{}).Paint(s, w, h, 9, scene.Rect{}).Draw(""); warm != cold {
					t.Fatal("cached weather differs from a fresh render of the current snapshot")
				}
			})
		}
	}
}

func TestConcurrentPaintAndHitKeepTheirOwnSnapshot(t *testing.T) {
	old := scene.Colors
	scene.Colors = scene.TrueColor
	t.Cleanup(func() { scene.Colors = old })
	type picture struct {
		s           scene.Scene
		w, h, frame int
		text        string
		hits        []string
	}
	points := [][2]int{{0, 0}, {40, 14}, {65, 21}, {79, 23}}
	var pictures []picture
	for i, weather := range []scene.Weather{scene.Clouds, scene.Storm, scene.Eruption, scene.Summit} {
		p := picture{s: lone(func(p *scene.Peak) { p.Weather = weather; p.Progress = float64(i+1) / 5 }), w: 80 + i*20, h: 24 + i*8, frame: 5 + i}
		p.text = (Kaiju{}).Paint(p.s, p.w, p.h, p.frame, scene.Rect{}).Draw("")
		for _, point := range points {
			p.hits = append(p.hits, (Kaiju{}).Hit(p.s, p.w, p.h, scene.Rect{}, point[0], point[1]))
		}
		pictures = append(pictures, p)
	}
	var workers sync.WaitGroup
	for worker := range 4 {
		workers.Go(func() {
			for i := range 12 {
				p := pictures[(worker+i)%len(pictures)]
				if got := (Kaiju{}).Paint(p.s, p.w, p.h, p.frame, scene.Rect{}).Draw(""); got != p.text {
					t.Error("concurrent paint used another snapshot's weather or geometry")
				}
				for j, point := range points {
					if got := (Kaiju{}).Hit(p.s, p.w, p.h, scene.Rect{}, point[0], point[1]); got != p.hits[j] {
						t.Errorf("concurrent hit %v = %q, want %q", point, got, p.hits[j])
					}
				}
			}
		})
	}
	workers.Wait()
}
