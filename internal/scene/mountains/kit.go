package mountains

import "github.com/project-kikkuli/yamnaya/internal/scene"

type rgb = scene.RGB

var white = scene.White

func hex(s string) rgb                                { return scene.Hex(s) }
func mix(a, b rgb, t float64) rgb                     { return scene.Mix(a, b, t) }
func clamp(v, lo, hi float64) float64                 { return scene.Clamp(v, lo, hi) }
func smooth(lo, hi, v float64) float64                { return scene.Smooth(lo, hi, v) }
func lerp(a, b, t float64) float64                    { return scene.Lerp(a, b, t) }
func readable(fg, bg rgb) (rgb, rgb)                  { return scene.Readable(fg, bg) }
func backing(fg, bg rgb) rgb                          { return scene.Backing(fg, bg) }
func seedOf(s string) uint64                          { return scene.SeedOf(s) }
func spread(seed uint64, i int) uint64                { return scene.Spread(seed, i) }
func unit(seed uint64, i int) float64                 { return scene.Unit(seed, i) }
func fbm(seed uint64, x float64, octaves int) float64 { return scene.Fbm(seed, x, octaves) }
func noise2(seed uint64, x, y float64) float64        { return scene.Noise2(seed, x, y) }
func fbm2(seed uint64, x, y float64, octaves int) float64 {
	return scene.Fbm2(seed, x, y, octaves)
}
