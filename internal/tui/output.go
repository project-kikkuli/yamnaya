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
	// sent and spent are the bytes and seconds of the writes that show how fast the terminal takes them; their ratio is
	// its rate.
	sent, spent float64
	// recent is how long the last writes took, the least of which is the terminal's latency whatever the write's size.
	recent [8]time.Duration
	writes int
	// held is whether the last write waited, and heldAt when writes last kept waiting.
	held   bool
	heldAt time.Time
	// burst is the bytes that have vanished since burstAt.
	burst   int
	burstAt time.Time
}

const (
	// bigWrite is more than a terminal's buffer swallows without waiting.
	bigWrite = 32 << 10
	// waited is how far past its latency a write has to run to have waited.
	waited = 5 * time.Millisecond
	// rateWindow is how many vanished bytes raise the rate by about e on a terminal not known to have room.
	rateWindow = 64 << 10
	// burstTime is the time in which vanishing bigWrite bytes shows a terminal has room: more than a slow link's buffer
	// and what it takes in that time.
	burstTime = 150 * time.Millisecond
	// clear is how long a terminal goes without waiting before the budget climbs quickly, up to clearBudget.
	clear       = 1500 * time.Millisecond
	clearBudget = 8 << 10
	// firstBudget is what a frame may send before anything is known of the terminal.
	firstBudget = 16 << 10
	// maxRate is more than any terminal takes.
	maxRate = 1 << 30
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
		o.judge(len(b), took)
		o.writing = false
		o.mu.Unlock()
		o.cond.Broadcast()
	}
}

// judge takes what a write of n bytes shows of the terminal's rate. A write big enough not to vanish shows it, and so do
// small ones that keep waiting; one small wait may be a hiccup. A write that used its budget and did not wait says
// there was room for that much. Only a burst no slow link could swallow shows there is room for far more, as does never
// having waited at all, which overshoots a slow link once at the start. Short of that the budget climbs slowly, or to
// clearBudget on a terminal that has not waited lately, which is all a quiet slow link can be sent without overrunning it.
func (o *output) judge(n int, took time.Duration) {
	if o.spent == 0 {
		o.sent, o.spent = firstBudget, frameTime.Seconds()
	}
	o.recent[o.writes%len(o.recent)] = took
	o.writes++
	latency := took
	for _, d := range o.recent[:min(o.writes, len(o.recent))] {
		latency = min(latency, d)
	}
	held := took-latency >= waited
	if held && (n >= bigWrite || o.held) {
		o.heldAt = time.Now()
	}
	budget := int(o.sent / o.spent * frameTime.Seconds())
	switch {
	case held:
		o.burst = 0
		if n >= bigWrite || o.held {
			o.sent = o.sent*3/4 + float64(n)
			o.spent = o.spent*3/4 + took.Seconds()
		}
	case n >= bigWrite:
		o.sent = o.sent*3/4 + float64(n)
		o.spent = o.spent*3/4 + took.Seconds()
	case 4*n >= budget:
		if time.Since(o.burstAt) > burstTime {
			o.burst, o.burstAt = 0, time.Now()
		}
		o.burst += n
		switch {
		case o.burst >= bigWrite || o.heldAt.IsZero():
			o.spent /= 4
		case budget < clearBudget && time.Since(o.heldAt) >= clear:
			o.spent /= 2
		default:
			o.spent /= 1 + float64(n)/rateWindow
		}
	}
	o.held = held
	o.spent = max(o.spent, o.sent/maxRate)
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
