package core

import (
	"errors"
	"strings"
	"time"
	"unicode"
)

// ErrNotComposing means the agent's input box did not show the pasted text with no dialog open, so Enter was not pressed.
var ErrNotComposing = errors.New("the agent's input box is not on screen")

// ErrSubmissionUncertain means a paste was attempted; the caller must never paste the message again.
var ErrSubmissionUncertain = errors.New("paste attempted, submission uncertain; not retyping")

// TypePoll is how often Type rereads the screen while the paste lands.
var TypePoll = 50 * time.Millisecond

// Type pastes text into the agent at h and presses Enter only once the screen shows its input box holding the text and
// neither the screen nor the harness reports a dialog. Pastes never answer a dialog in either harness, but Enter does.
func Type(host Host, h Handle, hn Harness, pid int, text string) error {
	if _, ok := composing(host, h, hn, pid); !ok {
		return ErrNotComposing
	}
	if err := host.Paste(h, text); err != nil {
		return errors.Join(ErrSubmissionUncertain, err)
	}
	want := typedTail(text)
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(TypePoll) {
		box, ok := composing(host, h, hn, pid)
		if !ok {
			return errors.Join(ErrSubmissionUncertain, ErrNotComposing)
		}
		if holds(box, want) {
			if err := host.Enter(h); err != nil {
				return errors.Join(ErrSubmissionUncertain, err)
			}
			for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(TypePoll) {
				box, ok := composing(host, h, hn, pid)
				if ok && !holds(box, want) {
					return nil
				}
			}
			return errors.Join(ErrSubmissionUncertain, errors.New("input still holds the paste after Enter"))
		}
	}
	return errors.Join(ErrSubmissionUncertain, ErrNotComposing)
}

// composing reads the input box when the screen shows one and no dialog.
func composing(host Host, h Handle, hn Harness, pid int) (string, bool) {
	return readComposer(host, h, hn, pid, false)
}

func readComposer(host Host, h Handle, hn Harness, pid int, idle bool) (string, bool) {
	if w, ok := hn.(Waiter); ok && w.Waiting(pid) {
		return "", false
	}
	screen, err := host.Screen(h)
	if err != nil {
		return "", false
	}
	if _, shown := hn.ParsePrompt(screen); shown {
		return "", false
	}
	if idle {
		if h, ok := hn.(interface{ IdleComposer(string) (string, bool) }); ok {
			return h.IdleComposer(screen)
		}
	}
	return hn.Composer(screen)
}

// typedTail is the end of text with whitespace removed, which survives the input box wrapping it.
func typedTail(text string) string {
	r := []rune(strings.Map(dropSpace, text))
	return string(r[max(0, len(r)-32):])
}

func holds(box, tail string) bool {
	box = strings.Map(dropSpace, box)
	return strings.Contains(box, tail) || strings.Contains(box, "[Pasted")
}

func dropSpace(r rune) rune {
	if unicode.IsSpace(r) {
		return -1
	}
	return r
}
