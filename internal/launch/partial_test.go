package launch

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
)

type partialHost struct {
	core.Host
	screen   string
	pasted   int
	failRead bool
	pasteErr bool
	enterErr bool
	stuck    bool
}

func (h *partialHost) Screen(core.Handle) (string, error) {
	if h.failRead && h.pasted > 0 {
		return "", fmt.Errorf("capture failed after paste")
	}
	return h.screen, nil
}
func (h *partialHost) Paste(_ core.Handle, text string) error {
	h.pasted++
	if h.pasteErr {
		return fmt.Errorf("paste transport failed")
	}
	h.screen = "› " + strings.ReplaceAll(text, "\n", "\n  ") + "\n\n  GPT-6.1-Sol high fast · /repo\n  tab to queue message\n"
	return nil
}
func (h *partialHost) Enter(core.Handle) error {
	if h.enterErr {
		return fmt.Errorf("Enter transport failed")
	}
	if !h.stuck {
		h.screen = "› Ask Codex to do anything\n\n  GPT-6.1-Sol high fast · /repo\n  tab to queue message\n"
	}
	return nil
}

type partialPane struct {
	host  *partialHost
	polls int
}

func (p *partialPane) Prompt() (bool, bool, error) {
	p.polls++
	return false, p.polls <= 2, nil
}
func (p *partialPane) Type(text string) error {
	p.host.screen = "• Working (6s • esc to interrupt)\n\n› Ask Codex to do anything\n\n  GPT-6.1-Sol high fast · /repo\n  tab to queue message\n"
	return core.Type(p.host, core.Handle{ID: "%1"}, &codex.Adapter{}, 0, text)
}

func TestPartialPasteIsNeverReturnedToTheQueue(t *testing.T) {
	for _, tc := range []struct {
		name  string
		host  partialHost
		state string
	}{
		{"capture fails after paste", partialHost{failRead: true}, core.MsgUncertain},
		{"paste transport returns error", partialHost{pasteErr: true}, core.MsgUncertain},
		{"Enter returns error", partialHost{enterErr: true}, core.MsgUncertain},
		{"Enter leaves paste in composer", partialHost{stuck: true}, core.MsgUncertain},
		{"multiline paste submitted", partialHost{}, core.MsgTyped},
	} {
		for _, held := range []bool{false, true} {
			t.Run(fmt.Sprint(tc.name, "/held=", held), func(t *testing.T) {
				st := store(t)
				snd := Sender{Box: st}
				h := tc.host
				p := &partialPane{host: &h}
				text := "ym: audit completed\n\n- Passed lock interoperability.\n- Reproduced the pending-resumer race.\n\nNo credentials changed."
				var m core.Message
				if held {
					m, _ = st.AddMessage("s1", text, core.MsgPending)
				} else {
					var err error
					m, _, err = snd.Send("s1", p, text)
					if err != nil {
						t.Fatal(err)
					}
				}
				_, holdErr := snd.Hold(m.Seq, p)
				if held && tc.state == core.MsgUncertain && !errors.Is(holdErr, core.ErrSubmissionUncertain) {
					t.Fatalf("Hold error %v", holdErr)
				}
				snd.Hold(m.Seq, p)
				got, _ := st.Message(m.Seq)
				if p.host.pasted != 1 || got.State != tc.state {
					t.Fatalf("pasted %d times, state %s; a successful paste followed by a failed composer check must never be replayed", p.host.pasted, got.State)
				}
			})
		}
	}
}
