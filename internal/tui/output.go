package tui

import (
	"io"
	"os"
	"os/exec"
	"sync"
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
}

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
		_, _ = o.to.Write(b)
		o.mu.Lock()
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
