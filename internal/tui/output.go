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
	// sent and spent are the bytes and seconds of the last few writes that waited or are too big to vanish into the
	// terminal's buffer; their ratio is its rate. Writes that vanish say only that there was room, so they raise it slowly.
	sent, spent float64
}

const (
	// bigWrite is more than a terminal's buffer swallows without waiting.
	bigWrite = 32 << 10
	// waited is how long a write takes once the buffer is full, longer than one that vanished.
	waited = 5 * time.Millisecond
	// rateWindow is how many vanished bytes raise the rate by about e.
	rateWindow = 64 << 10
	// firstBudget is what a frame may send before anything is known of the terminal.
	firstBudget = 16 << 10
)

// frameRate is how many frames a second the board renders.
const frameRate = 30

// frameTime is how long a key may wait behind the mountains: one frame.
const frameTime = time.Second / frameRate

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
		switch {
		case len(b) >= bigWrite || took >= waited:
			o.sent = o.sent*3/4 + float64(len(b))
			o.spent = o.spent*3/4 + took.Seconds()
		case o.spent == 0:
			o.sent, o.spent = firstBudget/frameTime.Seconds(), 1
		default:
			o.spent /= 1 + float64(len(b))/rateWindow
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

// budget is how many bytes the terminal takes within a frame, at least a few cells' worth.
func (o *output) budget() int {
	if o == nil {
		return math.MaxInt
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.spent == 0 {
		return firstBudget
	}
	return max(1<<9, int(o.sent/o.spent*frameTime.Seconds()))
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
