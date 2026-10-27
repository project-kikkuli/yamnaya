package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"regexp"
)

// termRelay passes what a remote session draws to the terminal unchanged, hands copy the text it sets the clipboard
// to, hands open each web link the remote's tmux asks to open here, and hands forward each port on the remote's own
// localhost its links name, once.
type termRelay struct {
	out     io.Writer
	copy    func(text []byte)
	open    func(url []byte)
	forward func(port string)
	carry   []byte
	ports   map[string]bool
}

const (
	oscClipboard = "\x1b]52;"
	// oscOpen is what ym's tmux on a host writes to the client's terminal when the owner right-clicks a link there.
	oscOpen      = "\x1b]7477;open;"
	maxClipboard = 1 << 20
)

var (
	localLink = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})`)
	webLink   = regexp.MustCompile(`^https?://[^\s\x00-\x1f\x7f]+$`)
)

func (r *termRelay) Write(p []byte) (int, error) {
	n, err := r.out.Write(p)
	buf := append(r.carry, p...)
	r.carry = nil
	if r.forward != nil {
		for _, m := range localLink.FindAllSubmatch(buf, -1) {
			if port := string(m[1]); !r.ports[port] {
				if r.ports == nil {
					r.ports = map[string]bool{}
				}
				r.ports[port] = true
				r.forward(port)
			}
		}
	}
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
			if r.open != nil && webLink.Match(body[:end]) {
				r.open(body[:end])
			}
		} else {
			r.clip(body[:end])
		}
		buf = body[end+term:]
	}
	r.carry = append([]byte(nil), buf[max(0, len(buf)-len(oscClipboard)-24):]...)
	return n, err
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
