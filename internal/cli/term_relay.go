package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"regexp"
	"strconv"
)

// termRelay passes what a remote session draws to the terminal unchanged, hands copy the text it sets the clipboard
// to, and for each web link the remote's tmux asks to open here at a cell clicked says the owner just right-clicked here,
// hands forward its port when it is on the remote's own localhost, once a port, then hands open the link.
type termRelay struct {
	out     io.Writer
	copy    func(text []byte)
	open    func(url []byte)
	clicked func(x, y int) bool
	forward func(port string)
	carry   []byte
	ports   map[string]bool
}

const (
	oscClipboard = "\x1b]52;"
	// oscOpen, then x;y;url, is what ym's tmux on a host writes to the client's terminal when the owner right-clicks a link at
	// that cell there, and what any program there can print, so only a right-click seen on this end's own input counts.
	oscOpen      = "\x1b]7477;open;"
	maxClipboard = 1 << 20
)

var (
	localLink = regexp.MustCompile(`^https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})`)
	webLink   = regexp.MustCompile(`^https?://[^\s\x00-\x1f\x7f]+$`)
)

func (r *termRelay) Write(p []byte) (int, error) {
	n, err := r.out.Write(p)
	buf := append(r.carry, p...)
	r.carry = nil
	for {
		at, prefix := bytes.Index(buf, []byte(oscClipboard)), oscClipboard
		if o := bytes.Index(buf, []byte(oscOpen)); o >= 0 && (at < 0 || o < at) {
			at, prefix = o, oscOpen
		}
		if at < 0 {
			break
		}
		body := buf[at+len(prefix):]
		end, term := bytes.IndexByte(body, '\a'), 1
		if st := bytes.Index(body, []byte("\x1b\\")); st >= 0 && (end < 0 || st < end) {
			end, term = st, 2
		}
		if end < 0 {
			if len(body) < maxClipboard {
				r.carry = append([]byte(nil), buf[at:]...)
			}
			return n, err
		}
		if prefix == oscOpen {
			r.request(body[:end])
		} else {
			r.clip(body[:end])
		}
		buf = body[end+term:]
	}
	r.carry = append([]byte(nil), buf[max(0, len(buf)-len(oscClipboard)-24):]...)
	return n, err
}

// request opens the x;y;url body of one open request when the owner right-clicked cell x, y here.
func (r *termRelay) request(body []byte) {
	xs, rest, _ := bytes.Cut(body, []byte(";"))
	ys, url, _ := bytes.Cut(rest, []byte(";"))
	x, errX := strconv.Atoi(string(xs))
	y, errY := strconv.Atoi(string(ys))
	if errX != nil || errY != nil || !webLink.Match(url) || r.clicked == nil || !r.clicked(x, y) {
		return
	}
	if m := localLink.FindSubmatch(url); m != nil && r.forward != nil && !r.ports[string(m[1])] {
		if r.ports == nil {
			r.ports = map[string]bool{}
		}
		r.ports[string(m[1])] = true
		r.forward(string(m[1]))
	}
	if r.open != nil {
		r.open(url)
	}
}

// clip decodes the selection;data body of one clipboard sequence; a ? asks for the clipboard and sets nothing.
func (r *termRelay) clip(body []byte) {
	_, data, ok := bytes.Cut(body, []byte(";"))
	if !ok || r.copy == nil || string(data) == "?" {
		return
	}
	text, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return
	}
	r.copy(text)
}
