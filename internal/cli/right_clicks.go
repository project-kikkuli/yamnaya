package cli

import (
	"bytes"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// clickWindow is how long after a right-click here a remote's request to open a link counts for it.
const clickWindow = 2 * time.Second

var mouseInput = regexp.MustCompile(`(?s)\x1b\[20([01])~|\x1b\[<2;(\d+);(\d+)M|\x1b\[M"(.)(.)`)

// rightClicks watches what the owner's terminal sends for a right-button press outside a paste, so a request to open a link
// that a remote sends back opens only for the cell clicked here, once, within clickWindow; nothing a remote prints reaches
// this input.
type rightClicks struct {
	mu      sync.Mutex
	at      time.Time
	x, y    int
	pasting bool
	carry   []byte
	now     func() time.Time
}

func (c *rightClicks) see(p []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := append(c.carry, p...)
	c.carry = nil
	done := 0
	for _, m := range mouseInput.FindAllSubmatchIndex(buf, -1) {
		done = m[1]
		switch {
		case m[2] >= 0:
			c.pasting = buf[m[2]] == '0'
		case c.pasting:
		case m[4] >= 0:
			x, _ := strconv.Atoi(string(buf[m[4]:m[5]]))
			y, _ := strconv.Atoi(string(buf[m[6]:m[7]]))
			c.press(x, y)
		default:
			c.press(int(buf[m[8]])-32, int(buf[m[10]])-32)
		}
	}
	if at := bytes.LastIndexByte(buf[done:], 0x1b); at >= 0 && len(buf)-done-at < 32 {
		c.carry = append([]byte(nil), buf[done+at:]...)
	}
}

func (c *rightClicks) press(x, y int) {
	c.at, c.x, c.y = c.clock(), x, y
}

// take reports whether the latest right-click, at cell x, y counting from 1, waits for its open, and spends it.
func (c *rightClicks) take(x, y int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	ok := !c.at.IsZero() && c.clock().Sub(c.at) <= clickWindow && x == c.x && y == c.y
	c.at = time.Time{}
	return ok
}

func (c *rightClicks) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
