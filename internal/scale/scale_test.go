package scale

import (
	"testing"
	"time"
)

// BenchmarkBoard is one second of a focused board over a thousand goals: the clock moves a second and the board reloads.
func BenchmarkBoard(b *testing.B) {
	r := Open(b, Thousand)
	Measure(b, func() {
		r.Advance(time.Second)
		if _, err := r.Core.Board(); err != nil {
			b.Fatal(err)
		}
	})
}

// BenchmarkBoardWhileWorking is the same second with one agent's transcript growing, as at least one always does.
func BenchmarkBoardWhileWorking(b *testing.B) {
	r := Open(b, Thousand)
	i := 0
	Measure(b, func() {
		r.Advance(time.Second)
		i++
		if err := r.Touch(i % len(r.Sessions)); err != nil {
			b.Fatal(err)
		}
		if _, err := r.Core.Board(); err != nil {
			b.Fatal(err)
		}
	})
}
