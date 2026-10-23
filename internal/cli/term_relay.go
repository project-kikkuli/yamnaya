package cli

import (
	"bytes"
	"encoding/base64"
	"io"
	"regexp"
)

// termRelay passes what a remote session draws to the terminal unchanged, hands copy the text it sets the clipboard
// to, and hands forward each port on the remote's own localhost its links name, once.
type termRelay struct {
	out     io.Writer
	copy    func(text []byte)
	forward func(port string)
	carry   []byte
	ports   map[string]bool
}

const (
	oscClipboard = "\x1b]52;"
	maxClipboard = 1 << 20
)

var localLink = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):(\d{2,5})`)

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
		at := bytes.Index(buf, []byte(oscClipboard))
		if at < 0 {
			break
		}
		body := buf[at+len(oscClipboard):]
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
		r.clip(body[:end])
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
