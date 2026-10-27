package cli

import (
	"bytes"
	"regexp"
	"sync"
	"time"
)

// clickWindow is how long after a right-click here a remote's request to open a link counts for it.
const clickWindow = 2 * time.Second

var rightPress = regexp.MustCompile(`(?s)\x1b\[<2;\d+;\d+M|\x1b\[M"..`)

// rightClicks watches what the owner's terminal sends for a right-button press, so a request to open a link that a remote
// sends back opens only for a click made here, once, within clickWindow; nothing a remote prints reaches this input.
type rightClicks struct {
	mu    sync.Mutex
	at    time.Time
	carry []byte
	now   func() time.Time
}

func (c *rightClicks) see(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := append(c.carry, p...)
	c.carry = nil
	done := 0
	for _, m := range rightPress.FindAllIndex(buf, -1) {
		c.at, done = c.clock(), m[1]
	}
	if at := bytes.LastIndexByte(buf[done:], 0x1b); at >= 0 && len(buf)-done-at < 32 {
		c.carry = append([]byte(nil), buf[done+at:]...)
	}
}

// take reports whether a right-click is waiting for its open, and spends it.
func (c *rightClicks) take() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ok := !c.at.IsZero() && c.clock().Sub(c.at) <= clickWindow
	c.at = time.Time{}
	return ok
}

func (c *rightClicks) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
