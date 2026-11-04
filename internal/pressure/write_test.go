package pressure

import (
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

const writtenRows = 200

// TestStateWriter is one real ym serve's writes, run by the test below until it is killed. Like ym serve it goes on past a
// failed write.
func TestStateWriter(t *testing.T) {
	dir := os.Getenv("PRESSURE_WRITER_DIR")
	if dir == "" {
		t.Skip("run by the concurrent writers test")
	}
	state := State{Active: true, Holding: true, Cause: "memory", Changed: make([]Change, writtenRows)}
	throttle := Throttle{Paused: make([]Paused, writtenRows)}
	for {
		state.Save(dir)
		throttle.Save(dir)
	}
}

func TestConcurrentWritersNeverShowAReaderAPartialFile(t *testing.T) {
	for _, c := range []struct {
		name string
		read func(dir string) (rows int, err error)
	}{
		{"overload.json", func(dir string) (int, error) {
			s, err := Read(dir)
			return len(s.Changed), err
		}},
		{"throttle.json", func(dir string) (int, error) {
			s, err := ReadThrottle(dir)
			return len(s.Paused), err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			for range 3 {
				cmd := exec.Command(os.Args[0], "-test.run=^TestStateWriter$")
				cmd.Env = append(os.Environ(), "PRESSURE_WRITER_DIR="+dir)
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					cmd.Process.Kill()
					cmd.Wait()
				})
			}
			whole, absent, failures := 0, 0, map[string]int{}
			for end := time.Now().Add(3 * time.Second); time.Now().Before(end); {
				rows, err := c.read(dir)
				switch {
				case err != nil:
					failures[err.Error()]++
				case rows == writtenRows:
					whole++
				case rows == 0:
					absent++
				default:
					failures[fmt.Sprintf("%d rows", rows)]++
				}
			}
			if len(failures) > 0 {
				t.Fatalf("%d whole reads, %d before any write, and failures: %v", whole, absent, failures)
			}
			if whole < 100 {
				t.Fatalf("only %d whole reads, %d before any write: the writers did not run", whole, absent)
			}
			t.Logf("%d whole reads, %d before any write", whole, absent)
		})
	}
}
