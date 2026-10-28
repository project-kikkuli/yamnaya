package launch

import (
	"errors"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Mailbox keeps the owner's follow-up messages.
type Mailbox interface {
	AddMessage(session, text, state string) (core.Message, error)
	Message(seq int64) (core.Message, error)
	Claim(seq int64, state string) (bool, error)
	Unclaim(seq int64) error
	FinishTyping(seq int64, state string) error
}

// Pane is the terminal on ym's server a session runs in.
type Pane interface {
	// Prompt reports whether a permission dialog shows; live is false once the agent has exited, and err is set when tmux could not say.
	Prompt() (shown, live bool, err error)
	Type(text string) error
}

// Outcome is how a sent message stands right after Send.
type Outcome int

const (
	Typed Outcome = iota
	// Held waits for a permission prompt, or anything else that hides the input box, to clear before it is typed.
	Held
	// Queued waits for the agent to read it from ym item inbox.
	Queued
	// Sent went straight to the session's own inbox, which reads it after the current turn.
	Sent
	Uncertain
)

func (o Outcome) String() string {
	return [...]string{"typed into its pane", "held until its permission prompt clears", "queued for its next ym item inbox", "sent to its session inbox", "paste attempted, submission uncertain; not retyping"}[o]
}

// Sender delivers follow-up messages: typed into a ym-server pane even mid-turn, else handed to the session's own inbox, else left for ym item inbox.
type Sender struct {
	Box  Mailbox
	Poll time.Duration
	// Inbox hands text to the session's own inbox when it runs off ym's server; nil or false leaves it for ym item inbox.
	Inbox func(text string) bool
	// Off reports whether the owner turned ym off for a session; Hold leaves its message waiting while it is.
	Off func(session string) bool
}

// Send delivers text to session; pane is nil when the session does not run on ym's server.
func (s Sender) Send(session string, pane Pane, text string) (core.Message, Outcome, error) {
	text = strings.TrimSpace(text)
	if pane == nil {
		if s.Inbox != nil && s.Inbox(text) {
			m, err := s.Box.AddMessage(session, text, core.MsgSent)
			return m, Sent, err
		}
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Queued, err
	}
	shown, live, err := pane.Prompt()
	switch {
	case err != nil || !live:
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Queued, err
	case shown:
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Held, err
	}
	m, err := s.Box.AddMessage(session, text, core.MsgUncertain)
	if err != nil {
		return m, Uncertain, err
	}
	terr := pane.Type(text)
	switch {
	case terr == nil:
		if err := s.Box.FinishTyping(m.Seq, core.MsgTyped); err != nil {
			return m, Uncertain, errors.Join(core.ErrSubmissionUncertain, err)
		}
		m.State = core.MsgTyped
	case errors.Is(terr, core.ErrSubmissionUncertain):
		return m, Uncertain, nil
	default:
		err = s.Box.Unclaim(m.Seq)
		m, _ = s.Box.Message(m.Seq)
		return m, Held, err
	}
	return m, Typed, nil
}

// Hold types a held message once the pane's permission prompt clears. It gives up, leaving the message to the inbox, when the agent exits
// or ym is off for its session, and stops when the inbox took it first; while tmux cannot say, it keeps waiting.
func (s Sender) Hold(seq int64, pane Pane) (bool, error) {
	for {
		m, err := s.Box.Message(seq)
		if err != nil || m.State != core.MsgPending {
			return false, err
		}
		shown, live, err := pane.Prompt()
		if err == nil && !live {
			return false, nil
		}
		if err == nil && !shown {
			if s.Off != nil && s.Off(m.Session) {
				return false, nil
			}
			ok, err := s.Box.Claim(seq, core.MsgUncertain)
			if err != nil || !ok {
				return false, err
			}
			terr := pane.Type(m.Text)
			if terr == nil {
				return true, s.Box.FinishTyping(seq, core.MsgTyped)
			}
			if errors.Is(terr, core.ErrSubmissionUncertain) {
				return false, terr
			}
			if err := s.Box.Unclaim(seq); err != nil {
				return false, err
			}
		}
		time.Sleep(s.Poll)
	}
}
