package tui

import "os"

// input reads the terminal's keys and hands a lone escape on as the key code for escape, so esc arrives at once
// instead of after the wait for the rest of an escape sequence that never comes.
type input struct{ *os.File }

const escapeKey = "\x1b[27u"

func (i input) Read(b []byte) (int, error) {
	n, err := i.File.Read(b)
	if n == 1 && b[0] == 0x1b && len(b) >= len(escapeKey) {
		return copy(b, escapeKey), err
	}
	return n, err
}
