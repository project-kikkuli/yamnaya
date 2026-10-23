package cli

import (
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scale"
)

// BenchmarkServeCycle is one cycle of ym serve over a thousand goals, five seconds after the last, and the GitHub calls it makes
// per minute at that pace.
func BenchmarkServeCycle(b *testing.B) {
	r := scale.Open(b, scale.Thousand)
	start := r.GitHub.Total()
	scale.Measure(b, func() {
		r.Advance(5 * time.Second)
		if err := serveCycle(r.Core); err != nil {
			b.Fatal(err)
		}
	})
	b.ReportMetric(float64(r.GitHub.Total()-start)/(float64(b.N)*5/60), "gh-calls/min")
}
