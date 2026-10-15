package launch

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// pane is a fake ym-server pane: whether a permission prompt shows on each read, and what was typed.
type pane struct {
	prompts []bool
	dead    bool
	fail    bool
	typed   []string
}

func (p *pane) Prompt() (bool, bool) {
	if p.dead {
		return false, false
	}
	shown := p.prompts[0]
	if len(p.prompts) > 1 {
		p.prompts = p.prompts[1:]
	}
	return shown, true
}

func (p *pane) Type(text string) error {
	if p.fail {
		return errors.New("tmux failed")
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
		{"typing failed", &pane{prompts: []bool{false}, fail: true}, Queued, core.MsgPending, 0},
		{"not on ym's server", nil, Queued, core.MsgPending, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store(t)
			var p Pane
			if c.pane != nil {
				p = c.pane
			}
			m, out, err := Sender{Box: st}.Send("s1", p, "  also\n rebase  ")
			if err != nil || out != c.out || m.State != c.state || m.Text != "also rebase" {
				t.Fatalf("Send = %+v, %v, %v", m, out, err)
			}
			if c.pane != nil && len(c.pane.typed) != c.typed {
				t.Fatalf("typed %q", c.pane.typed)
			}
			taken, _ := st.TakeMessages("s1")
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
	}{
		{"prompt clears", &pane{prompts: []bool{true, true, true, false}}, false, true, core.MsgTyped, true},
		{"agent exits first", &pane{dead: true}, false, false, core.MsgPending, false},
		{"inbox took it first", &pane{prompts: []bool{false}}, true, false, core.MsgInbox, false},
		{"typing fails", &pane{prompts: []bool{false}, fail: true}, false, false, core.MsgPending, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := store(t)
			snd := Sender{Box: st}
			m, _ := st.AddMessage("s1", "also rebase", core.MsgPending)
			if c.inbox {
				st.TakeMessages("s1")
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
