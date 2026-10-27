package themes

import (
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
	"golang.org/x/sys/unix"
)

const (
	budgetW, budgetH = 200, 60
	// frameBudget is the most a frame may take to paint on the owner's Mac, in the CPU time of the thread painting
	// it so other work on the machine does not count, judged at its 90th percentile so one GC pause does not fail
	// it. A slower machine gets a budget as much bigger as it is slower at reference.
	frameBudget  = 2 * time.Millisecond
	budgetFrames = 120
	// referenceCPU is what reference takes on the owner's Mac (Apple M4 Pro).
	referenceCPU = 660 * time.Microsecond
)

// reference is a fixed piece of the arithmetic themes do, timed to scale the budget to the machine.
func reference(t *testing.T) time.Duration {
	best := time.Hour
	for range 7 {
		start, sum := cpu(t), 0.0
		for i := range 20000 {
			sum += scene.Fbm2(7, float64(i)*.37, float64(i)*.11, 4)
		}
		if sum < 0 {
			t.Fatal(sum)
		}
		best = min(best, cpu(t)-start)
	}
	return best
}

func TestEveryThemePaintsABusyBoardWithinBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("timing")
	}
	runtime.LockOSThread()
	ref := reference(t)
	runtime.UnlockOSThread()
	budget := time.Duration(float64(frameBudget) * max(1, float64(ref)/float64(referenceCPU)))
	t.Logf("reference %v (%v on the owner's Mac): frame budget %v", ref, referenceCPU, budget)
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			s, box := busy(th.Name(), .5), scene.TableBox(table(20), budgetW, budgetH)
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			th.Paint(s, budgetW, budgetH, 0, box)
			times := make([]time.Duration, budgetFrames)
			for f := range times {
				start := cpu(t)
				th.Paint(s, budgetW, budgetH, f+1, box)
				times[f] = cpu(t) - start
			}
			slices.Sort(times)
			t.Logf("frame CPU: median %v, 90th percentile %v, budget %v", times[len(times)/2], times[len(times)*9/10], budget)
			if p90 := times[len(times)*9/10]; p90 > budget {
				t.Errorf("90th percentile frame %v, budget %v (median %v)", p90, budget, times[len(times)/2])
			}
		})
	}
}

func TestNoThemeAllocatesMoreAsFramesGoBy(t *testing.T) {
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			s, box := busy(th.Name(), .5), scene.TableBox(table(20), budgetW, budgetH)
			allocs := func(from int) uint64 {
				var a, b runtime.MemStats
				runtime.GC()
				runtime.ReadMemStats(&a)
				for f := from; f < from+40; f++ {
					th.Paint(s, budgetW, budgetH, f, box)
				}
				runtime.ReadMemStats(&b)
				return (b.Mallocs - a.Mallocs) / 40
			}
			allocs(0)
			early, late := allocs(1), allocs(4001)
			if late > early+early/10+16 {
				t.Errorf("allocations per frame grew from %d to %d", early, late)
			}
		})
	}
}

func BenchmarkTheme(b *testing.B) {
	for _, th := range scene.Themes() {
		b.Run(th.Name(), func(b *testing.B) {
			s, box := busy(th.Name(), .5), scene.TableBox(table(20), budgetW, budgetH)
			th.Paint(s, budgetW, budgetH, 0, box)
			b.ReportAllocs()
			b.ResetTimer()
			for i := range b.N {
				th.Paint(s, budgetW, budgetH, i+1, box)
			}
		})
	}
}

// cpu is the CPU time the calling thread has used.
func cpu(t *testing.T) time.Duration {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_THREAD_CPUTIME_ID, &ts); err != nil {
		t.Fatal(err)
	}
	return time.Duration(ts.Nano())
}
