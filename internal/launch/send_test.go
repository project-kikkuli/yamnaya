package launch

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// pane is a fake ym-server pane: whether a permission prompt shows on each read, how many typings find no input box, and what was typed.
type pane struct {
	prompts []bool
	dead    bool
	fails   int
	typed   []string
	// unread are the tmux errors the next prompt reads fail with, one each.
	unread []error
}

func (p *pane) Prompt() (bool, bool, error) {
	if len(p.unread) > 0 {
		err := p.unread[0]
		p.unread = p.unread[1:]
		return false, false, err
	}
	if p.dead {
		return false, false, nil
	}
	shown := p.prompts[0]
	if len(p.prompts) > 1 {
		p.prompts = p.prompts[1:]
	}
	return shown, true, nil
}

func (p *pane) Type(text string) error {
	if p.fails > 0 {
		p.fails--
		return core.ErrNotComposing
	}
	p.typed = append(p.typed, text)
	return nil
}

func store(t *testing.T) *core.Store {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSendTypesIntoAFreePaneAndQueuesTheRest(t *testing.T) {
	const message = "ym: agent a1 is idle. Its last message, verbatim: Parser done.\n\nOpen for you\n1. Keep the old dialect?\n2. Drop BOM handling?"
	for _, c := range []struct {
		name  string
		pane  *pane
		out   Outcome
		state string
		typed int
	}{
		{"idle or busy pane", &pane{prompts: []bool{false}}, Typed, core.MsgTyped, 1},
		{"permission prompt", &pane{prompts: []bool{true}}, Held, core.MsgPending, 0},
		{"agent exited", &pane{dead: true}, Queued, core.MsgPending, 0},
		{"tmux cannot say", &pane{unread: []error{errors.New("tmux -L ym display-message: signal: killed")}, prompts: []bool{false}}, Queued, core.MsgPending, 0},
		{"input box hidden", &pane{prompts: []bool{false}, fails: 1}, Held, core.MsgPending, 0},
		{"not on ym's server", nil, Queued, core.MsgPending, 0},
		{"not on ym's server, its inbox takes it", nil, Sent, core.MsgSent, 0},
		{"not on ym's server, its inbox refuses", nil, Queued, core.MsgPending, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store(t)
			var p Pane
			if c.pane != nil {
				p = c.pane
			}
			snd := Sender{Box: st}
			var handed []string
			if strings.HasPrefix(c.name, "not on ym's server, ") {
				snd.Inbox = func(text string) bool {
					handed = append(handed, text)
					return c.out == Sent
				}
			}
			m, out, err := snd.Send("s1", p, "  "+message+"\n")
			if err != nil || out != c.out || m.State != c.state || m.Text != message {
				t.Fatalf("Send = %+v, %v, %v", m, out, err)
			}
			if snd.Inbox != nil && (len(handed) != 1 || handed[0] != message) {
				t.Fatalf("handed the session inbox %q", handed)
			}
			if c.pane != nil && (len(c.pane.typed) != c.typed || c.typed == 1 && c.pane.typed[0] != message) {
				t.Fatalf("typed %q", c.pane.typed)
			}
			taken, _ := (&core.Core{Store: st}).TakeMessages("s1")
			if want := c.state == core.MsgPending; (len(taken) == 1) != want {
				t.Fatalf("inbox took %v; pending message should go to the inbox exactly when not typed", taken)
			}
		})
	}
}

func TestHoldTypesOnceThePromptClears(t *testing.T) {
	for _, c := range []struct {
		name    string
		pane    *pane
		inbox   bool
		typed   bool
		state   string
		claimed bool
		off     bool
	}{
		{"prompt clears", &pane{prompts: []bool{true, true, true, false}}, false, true, core.MsgTyped, true, false},
		{"prompt clears after ym was turned off", &pane{prompts: []bool{true, true, true, false}}, false, false, core.MsgPending, false, true},
		{"agent exits first", &pane{dead: true}, false, false, core.MsgPending, false, false},
		{"tmux cannot say, then the prompt clears", &pane{unread: []error{errors.New("killed"), errors.New("not found")}, prompts: []bool{false}}, false, true, core.MsgTyped, true, false},
		{"inbox took it first", &pane{prompts: []bool{false}}, true, false, core.MsgInbox, false, false},
		{"input box hidden until it shows", &pane{prompts: []bool{false}, fails: 2}, false, true, core.MsgTyped, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store(t)
			snd := Sender{Box: st, Off: func(session string) bool { return c.off && session == "s1" }}
			m, _ := st.AddMessage("s1", "also rebase", core.MsgPending)
			if c.inbox {
				(&core.Core{Store: st}).TakeMessages("s1")
			}
			ok, err := snd.Hold(m.Seq, c.pane)
			if err != nil || ok != c.claimed {
				t.Fatalf("Hold = %v, %v", ok, err)
			}
			got, _ := st.Message(m.Seq)
			if got.State != c.state || (len(c.pane.typed) == 1) != c.typed {
				t.Fatalf("message %+v, typed %q", got, c.pane.typed)
			}
		})
	}
}
