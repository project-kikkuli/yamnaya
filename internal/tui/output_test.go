package tui

import (
	"bytes"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestOutputNeverHoldsUpTheWriterAndKeepsOrder(t *testing.T) {
	for _, stalled := range []bool{false, true} {
		t.Run(map[bool]string{false: "reading terminal", true: "stalled terminal"}[stalled], func(t *testing.T) {
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			o := newOutput(w)
			resume := make(chan struct{})
			got := make(chan []byte)
			go func() {
				if stalled {
					<-resume
				}
				b, _ := io.ReadAll(r)
				got <- b
			}()
			var want bytes.Buffer
			wrote := make(chan struct{})
			go func() {
				for i := range 64 {
					frame := bytes.Repeat([]byte{byte('a' + i%26)}, 64<<10)
					want.Write(frame)
					_, _ = o.Write(frame)
				}
				close(wrote)
			}()
			select {
			case <-wrote:
			case <-time.After(30 * time.Second):
				t.Fatal("writes waited on the terminal")
			}
			if stalled && !o.behind() {
				t.Fatal("not behind while the terminal reads nothing")
			}
			close(resume)
			o.drain()
			if o.behind() {
				t.Fatal("behind after draining")
			}
			w.Close()
			if b := <-got; !bytes.Equal(b, want.Bytes()) {
				t.Fatalf("terminal got %d bytes out of order or short of %d", len(b), want.Len())
			}
		})
	}
}

// slowTerminal swallows room bytes at once and then takes rate bytes a second, or everything when rate is 0.
type slowTerminal struct {
	mu    sync.Mutex
	rate  float64
	room  float64
	level float64
	last  time.Time
}

func (s *slowTerminal) set(rate float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rate = rate
}

func (s *slowTerminal) Write(b []byte) (int, error) {
	for left := float64(len(b)); left > 0; {
		s.mu.Lock()
		now := time.Now()
		if s.rate == 0 {
			s.level, s.last = 0, now
			s.mu.Unlock()
			break
		}
		s.level = max(0, s.level-now.Sub(s.last).Seconds()*s.rate)
		s.last = now
		took := min(left, s.room-s.level)
		s.level += took
		left -= took
		s.mu.Unlock()
		if left > 0 {
			time.Sleep(2 * time.Millisecond)
		}
	}
	return len(b), nil
}

func TestBudgetFollowsWhatTheTerminalTakes(t *testing.T) {
	const fastBurst = 100 << 10
	for _, tc := range []struct {
		name   string
		rate   float64
		change func(s *slowTerminal)
		min    int
		max    int
	}{
		{name: "40 KB/s", rate: 40e3, min: 40000 / frameRate / 4, max: 40000 / frameRate * 3},
		{name: "100 KB/s", rate: 100e3, min: 100000 / frameRate / 4, max: 100000 / frameRate * 3},
		{name: "fast", min: fastBurst},
		{name: "slow link recovers", rate: 40e3, change: func(s *slowTerminal) { s.set(0) }, min: fastBurst},
		{name: "fast terminal stalls once", change: func(s *slowTerminal) {
			s.mu.Lock()
			s.rate, s.level, s.last = 10e3, 20e3, time.Now()
			s.mu.Unlock()
			time.AfterFunc(300*time.Millisecond, func() { s.set(0) })
		}, min: fastBurst},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			term := &slowTerminal{rate: tc.rate, room: 20 << 10, last: time.Now()}
			o := newOutputTo(nil, term)
			frames := func(d time.Duration) []int {
				var seen []int
				for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(frameTime) {
					if o.behind() {
						continue
					}
					n := o.budget()
					seen = append(seen, n)
					_, _ = o.Write(make([]byte, min(n, 32<<10)))
				}
				return seen
			}
			frames(2 * time.Second)
			if tc.change != nil {
				tc.change(term)
				_, _ = o.Write(make([]byte, 64<<10))
				frames(time.Second)
			}
			seen := frames(time.Second)
			if len(seen) == 0 {
				t.Fatal("the terminal took nothing for a second")
			}
			slices.Sort(seen)
			got := seen[len(seen)/2]
			if got < tc.min || (tc.max > 0 && got > tc.max) {
				t.Fatalf("median budget %d bytes a frame, want %d to %d", got, tc.min, tc.max)
			}
		})
	}
}
