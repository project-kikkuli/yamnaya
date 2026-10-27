package cli

import (
	"bytes"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"regexp"
)

// termRelay passes what a remote session draws to the terminal unchanged, hands copy the text it sets the clipboard
// to, hands open each web link a right-click on the remote asks to open here, and hands forward each port on the remote's
// own localhost its links name, once. An open counts only when it carries the nonce the remote ym announced as the first
// escape sequence of the stream, before any pane could print one.
type termRelay struct {
	out     io.Writer
	copy    func(text []byte)
	open    func(url []byte)
	forward func(port string)
	carry   []byte
	ports   map[string]bool
	nonce   []byte
	escaped bool
}

const (
	oscClipboard = "\x1b]52;"
	// oscNonce is what the ym ssh runs on a host announces first; oscOpen, nonce;url, is what a right-click on a link there sends.
	oscNonce     = "\x1b]7477;nonce;"
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
		at, prefix := -1, ""
		for _, p := range []string{oscClipboard, oscOpen, oscNonce} {
			if o := bytes.Index(buf, []byte(p)); o >= 0 && (at < 0 || o < at) {
				at, prefix = o, p
			}
		}
		if at < 0 {
			break
		}
		first := !r.escaped && bytes.IndexByte(buf[:at], 0x1b) < 0
		body := buf[at+len(prefix):]
		end, term := bytes.IndexByte(body, '\a'), 1
		if st := bytes.Index(body, []byte("\x1b\\")); st >= 0 && (end < 0 || st < end) {
			end, term = st, 2
		}
		if end < 0 {
			r.escaped = !first
			if len(body) < maxClipboard {
				r.carry = append([]byte(nil), buf[at:]...)
			}
			return n, err
		}
		r.escaped = true
		switch prefix {
		case oscNonce:
			if first {
				r.nonce = append([]byte(nil), body[:end]...)
			}
		case oscOpen:
			nonce, url, _ := bytes.Cut(body[:end], []byte(";"))
			if r.open != nil && len(r.nonce) > 0 && subtle.ConstantTimeCompare(nonce, r.nonce) == 1 && webLink.Match(url) {
				r.open(url)
			}
		default:
			r.clip(body[:end])
		}
		buf = body[end+term:]
	}
	keep := max(0, len(buf)-len(oscClipboard)-24)
	r.escaped = r.escaped || bytes.IndexByte(buf[:keep], 0x1b) >= 0
	r.carry = append([]byte(nil), buf[keep:]...)
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
