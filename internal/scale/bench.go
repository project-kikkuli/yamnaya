package scale

import (
	"os"
	"runtime/pprof"
	"syscall"
	"testing"
	"time"
)

// Open builds the rig for a benchmark in a temporary directory, or in $YM_SCALE_DIR so repeated runs skip writing it, and reads
// its board once so the benchmark measures steady state.
func Open(b *testing.B, s Spec) *Rig {
	b.Helper()
	dir := os.Getenv("YM_SCALE_DIR")
	if dir == "" {
		dir = b.TempDir()
	}
	r, err := Build(dir, s)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { r.Close() })
	if _, err := r.Core.Board(); err != nil {
		b.Fatal(err)
	}
	return r
}

// Measure runs op b.N times and reports the CPU it took, user and system, which a loaded machine does not inflate the way it
// inflates wall time. $YM_SCALE_PROFILE names a CPU profile of the measured ops alone.
func Measure(b *testing.B, op func()) {
	b.Helper()
	if path := os.Getenv("YM_SCALE_PROFILE"); path != "" {
		f, err := os.Create(path)
		if err != nil {
			b.Fatal(err)
		}
		if err := pprof.StartCPUProfile(f); err != nil {
			b.Fatal(err)
		}
		defer func() { pprof.StopCPUProfile(); f.Close() }()
	}
	before := cpu()
	b.ResetTimer()
	for b.Loop() {
		op()
	}
	b.StopTimer()
	b.ReportMetric(float64(cpu()-before)/float64(b.N)/float64(time.Millisecond), "cpu-ms/op")
}

func cpu() time.Duration {
	var u syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &u)
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}
