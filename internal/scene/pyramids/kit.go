package pyramids

import "github.com/project-kikkuli/yamnaya/internal/scene"

type rgb = scene.RGB

var white = scene.White

func hex(s string) rgb                     { return scene.Hex(s) }
func mix(a, b rgb, t float64) rgb          { return scene.Mix(a, b, t) }
func clamp(v, lo, hi float64) float64      { return scene.Clamp(v, lo, hi) }
func smooth(lo, hi, v float64) float64     { return scene.Smooth(lo, hi, v) }
func lerp(a, b, t float64) float64         { return scene.Lerp(a, b, t) }
func spread(seed uint64, i int) uint64     { return scene.Spread(seed, i) }
func unit(seed uint64, i int) float64      { return scene.Unit(seed, i) }
func noise(seed uint64, x float64) float64 { return scene.Noise(seed, x) }
func fbm2(seed uint64, x, y float64, octaves int) float64 {
	return scene.Fbm2(seed, x, y, octaves)
}
