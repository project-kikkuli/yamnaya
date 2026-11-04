package tui

import (
	"bytes"
	"fmt"
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

func TestAFastTerminalKeepsItsBudgetHoweverLongWritesVanish(t *testing.T) {
	for _, size := range []int{1 << 10, 16 << 10, 64 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			term := &slowTerminal{room: 20 << 10, last: time.Now()}
			o := newOutputTo(nil, term)
			frame := make([]byte, size)
			for sent := 0; sent < 64<<20; sent += size {
				_, _ = o.Write(frame)
				o.drain()
				if n := o.budget(); n < firstBudget {
					t.Fatalf("budget %d bytes a frame after %d bytes vanished", n, sent)
				}
			}
			term.set(40e3)
			for end := time.Now().Add(3 * time.Second); o.budget() > 40000/frameRate*3; time.Sleep(frameTime) {
				if time.Now().After(end) {
					t.Fatalf("budget still %d bytes a frame three seconds into a 40 KB/s link", o.budget())
				}
				if !o.behind() {
					_, _ = o.Write(make([]byte, min(o.budget(), 32<<10)))
				}
			}
		})
	}
}

// delayed takes as long as delay says over the nth write, whatever its size.
type delayed struct {
	mu    sync.Mutex
	n     int
	delay func(n int) time.Duration
}

func (d *delayed) Write(b []byte) (int, error) {
	d.mu.Lock()
	wait := d.delay(d.n)
	d.n++
	d.mu.Unlock()
	time.Sleep(wait)
	return len(b), nil
}

func TestATerminalThatIsOnlySlowToAnswerKeepsItsBudget(t *testing.T) {
	for _, latency := range []time.Duration{40 * time.Millisecond, 100 * time.Millisecond} {
		t.Run(latency.String(), func(t *testing.T) {
			t.Parallel()
			o := newOutputTo(nil, &delayed{delay: func(int) time.Duration { return latency }})
			for range int(2 * time.Second / latency) {
				_, _ = o.Write(make([]byte, min(o.budget(), 256<<10)))
				o.drain()
			}
			if got := o.budget(); got < firstBudget {
				t.Fatalf("budget %d bytes a frame with nothing limiting what the terminal takes", got)
			}
		})
	}
}

func TestOneHiccupHardlyMovesTheBudget(t *testing.T) {
	for _, size := range []int{100, 8 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			hiccup := func(n int) time.Duration {
				if n == 0 {
					return 10 * time.Millisecond
				}
				return 0
			}
			o := newOutputTo(nil, &delayed{delay: hiccup})
			_, _ = o.Write(make([]byte, size))
			o.drain()
			if got := o.budget(); got < firstBudget/2 {
				t.Fatalf("budget %d bytes a frame after one slow write of %d bytes", got, size)
			}
		})
	}
}

func TestAWholeSceneReachesAFastTerminalInAFewFrames(t *testing.T) {
	o := newOutputTo(nil, io.Discard)
	const scene = 10000
	frames := 0
	for cells := scene; cells > 0; frames++ {
		n := max(1, o.budget()/cellBytes)
		_, _ = o.Write(make([]byte, min(n, cells)*cellBytes))
		o.drain()
		cells -= n
	}
	if frames > 6 {
		t.Fatalf("%d frames to draw %d cells", frames, scene)
	}
}

func TestTheBudgetComesBackQuicklyAfterAStall(t *testing.T) {
	for _, size := range []int{1 << 10, 128 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			t.Parallel()
			stall := func(n int) time.Duration {
				if n == 20 {
					return time.Second
				}
				return 0
			}
			o := newOutputTo(nil, &delayed{delay: stall})
			for range 20 {
				_, _ = o.Write(make([]byte, min(o.budget()/2, 32<<10)))
				o.drain()
			}
			_, _ = o.Write(make([]byte, size))
			o.drain()
			frames := 0
			for ; o.budget() < firstBudget && frames < 100; frames++ {
				_, _ = o.Write(make([]byte, o.budget()/2))
				o.drain()
			}
			if frames > 12 {
				t.Fatalf("%d frames to get back to %d bytes a frame after a stall", frames, firstBudget)
			}
		})
	}
}
