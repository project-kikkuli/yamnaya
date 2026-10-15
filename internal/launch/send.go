package launch

import (
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
}

// Pane is the terminal on ym's server a session runs in.
type Pane interface {
	// Prompt reports whether a permission dialog shows; live is false once the agent has exited.
	Prompt() (shown, live bool)
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
)

func (o Outcome) String() string {
	return [...]string{"typed into its pane", "held until its permission prompt clears", "queued for its next ym item inbox", "sent to its session inbox"}[o]
}

// Sender delivers follow-up messages: typed into a ym-server pane even mid-turn, else handed to the session's own inbox, else left for ym item inbox.
type Sender struct {
	Box  Mailbox
	Poll time.Duration
	// Inbox hands text to the session's own inbox when it runs off ym's server; nil or false leaves it for ym item inbox.
	Inbox func(text string) bool
}

// Send delivers text to session; pane is nil when the session does not run on ym's server.
func (s Sender) Send(session string, pane Pane, text string) (core.Message, Outcome, error) {
	text = strings.Join(strings.Fields(text), " ")
	if pane == nil {
		if s.Inbox != nil && s.Inbox(text) {
			m, err := s.Box.AddMessage(session, text, core.MsgSent)
			return m, Sent, err
		}
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Queued, err
	}
	shown, live := pane.Prompt()
	switch {
	case !live:
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Queued, err
	case shown:
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Held, err
	}
	if pane.Type(text) != nil {
		m, err := s.Box.AddMessage(session, text, core.MsgPending)
		return m, Held, err
	}
	m, err := s.Box.AddMessage(session, text, core.MsgTyped)
	return m, Typed, err
}

// Hold types a held message once the pane's permission prompt clears. It gives up, leaving the message to the inbox, when the agent exits, and stops when the inbox took it first.
func (s Sender) Hold(seq int64, pane Pane) (bool, error) {
	for {
		m, err := s.Box.Message(seq)
		if err != nil || m.State != core.MsgPending {
			return false, err
		}
		shown, live := pane.Prompt()
		if !live {
			return false, nil
		}
		if !shown {
			ok, err := s.Box.Claim(seq, core.MsgTyped)
			if err != nil || !ok {
				return false, err
			}
			if pane.Type(m.Text) == nil {
				return true, nil
			}
			if err := s.Box.Unclaim(seq); err != nil {
				return false, err
			}
		}
		time.Sleep(s.Poll)
	}
}
