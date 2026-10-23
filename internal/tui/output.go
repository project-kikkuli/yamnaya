package tui

import (
	"io"
	"math"
	"os"
	"os/exec"
	"sync"
	"time"
)

// output writes the board to its terminal from a goroutine of its own, so a terminal slow to read never holds up
// keys; the mountains wait while it is behind.
type output struct {
	*os.File
	to      io.Writer
	mu      sync.Mutex
	cond    *sync.Cond
	pending []byte
	writing bool
	// rate is how many bytes a second the terminal has been taking, from writes too big to vanish into its buffer.
	rate float64
}

// frameTime is how long a key may wait behind the mountains: one frame.
const frameTime = 16 * time.Millisecond

func newOutput(f *os.File) *output { return newOutputTo(f, f) }

// newOutputTo is an output for terminal f whose bytes go to w.
func newOutputTo(f *os.File, w io.Writer) *output {
	o := &output{File: f, to: w}
	o.cond = sync.NewCond(&o.mu)
	go o.run()
	return o
}

func (o *output) Write(b []byte) (int, error) {
	o.mu.Lock()
	o.pending = append(o.pending, b...)
	o.mu.Unlock()
	o.cond.Broadcast()
	return len(b), nil
}

func (o *output) run() {
	for {
		o.mu.Lock()
		for len(o.pending) == 0 {
			o.cond.Wait()
		}
		b := o.pending
		o.pending, o.writing = nil, true
		o.mu.Unlock()
		start := time.Now()
		_, _ = o.to.Write(b)
		took := time.Since(start)
		o.mu.Lock()
		if len(b) >= 1<<10 {
			sample := float64(len(b)) / max(took, time.Millisecond).Seconds()
			if o.rate == 0 {
				o.rate = sample
			}
			o.rate += (sample - o.rate) / 4
		}
		o.writing = false
		o.mu.Unlock()
		o.cond.Broadcast()
	}
}

// behind reports whether the terminal has yet to take everything written to it.
func (o *output) behind() bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.writing || len(o.pending) > 0
}

// budget is how many bytes the terminal takes within a frame, at least enough to measure it by.
func (o *output) budget() int {
	if o == nil {
		return math.MaxInt
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.rate == 0 {
		return 16 << 10
	}
	return max(3<<9, int(o.rate*frameTime.Seconds()))
}

// drain waits until the terminal has taken everything written to it.
func (o *output) drain() {
	if o == nil {
		return
	}
	o.mu.Lock()
	for o.writing || len(o.pending) > 0 {
		o.cond.Wait()
	}
	o.mu.Unlock()
}

// attached runs a command on the terminal itself once everything the board wrote has reached it.
type attached struct {
	*exec.Cmd
	out *output
}

func (c attached) Run() error {
	c.out.drain()
	return c.Cmd.Run()
}

func (c attached) SetStdin(r io.Reader) {
	if in, ok := r.(input); ok {
		r = in.File
	}
	if c.Stdin == nil {
		c.Stdin = r
	}
}

func (c attached) SetStdout(w io.Writer) {
	if c.Stdout == nil {
		c.Stdout = c.out.terminal(w)
	}
}

func (c attached) SetStderr(w io.Writer) {
	if c.Stderr == nil {
		c.Stderr = w
	}
}

// terminal is the file a command run on the board's terminal writes to.
func (o *output) terminal(w io.Writer) io.Writer {
	if o == nil {
		return w
	}
	return o.File
}
