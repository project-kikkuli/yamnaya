package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/health"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// ownerSession is an idle Claude session the owner started, discovered by the real adapter and laid on a real board.
type ownerSession struct {
	dir, path string
	at        time.Time
	core      *core.Core
}

func newOwnerSession(t *testing.T) *ownerSession {
	t.Helper()
	dir := t.TempDir()
	o := &ownerSession{dir: dir, path: filepath.Join(dir, "projects", "-Users-me-app", sid+".jsonl"), at: time.Date(2026, 10, 22, 2, 0, 0, 0, time.UTC)}
	must(t, os.MkdirAll(filepath.Dir(o.path), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "sessions"), 0o755))
	write(t, filepath.Join(dir, "sessions", "7.json"), `{"pid":7,"sessionId":"`+sid+`","cwd":"/Users/me/app","status":"idle"}`)
	write(t, o.path, "")
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	must(t, err)
	t.Cleanup(func() { st.Close() })
	o.core = &core.Core{Store: st, Ask: health.Ask, Harnesses: []core.Harness{&Adapter{Dir: dir, Now: time.Now}},
		Procs: func() ([]proc.Proc, error) { return []proc.Proc{p(7, "claude")}, nil }}
	return o
}

func (o *ownerSession) add(t *testing.T, kind, text string) {
	t.Helper()
	o.at = o.at.Add(time.Minute)
	raw, _ := json.Marshal(text)
	line := fmt.Sprintf(`{"type":"user","timestamp":%q,"origin":{"kind":"human"},"message":{"content":%s}}`, o.at.Format(time.RFC3339), raw)
	if kind == "assistant" {
		line = fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"text","text":%s}],"stop_reason":"end_turn"}}`, o.at.Format(time.RFC3339), raw)
	}
	f, err := os.OpenFile(o.path, os.O_APPEND|os.O_WRONLY, 0o644)
	must(t, err)
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	must(t, err)
}

func (o *ownerSession) waiting(t *testing.T) []core.Need {
	t.Helper()
	b, err := o.core.Board()
	must(t, err)
	var out []core.Need
	for _, n := range b.Needs {
		if n.Waiting() {
			out = append(out, n)
		}
	}
	return out
}

// The D119 case, redacted: the agent asked item 4 in a numbered block, then kept replying "answer item 4" without it.
const loginAsk = "Step 3 is done: all three tools are logged in on the box.\n\n" +
	"**One thing to flag:** Claude on the box is logged in as a personal account, not the work one.\n\n" +
	"Open for you:\n4. **Claude login on the box:** keep the personal Max account, or switch to the work account. To switch, run `claude auth login` on the box yourself.\n\n" +
	"Next: answer item 4 with \"keep\" or \"switch\"."

func TestAFollowUpThatOnlyNamesAnOpenQuestionCarriesTheBlockThatAskedIt(t *testing.T) {
	o := newOwnerSession(t)
	o.add(t, "user", "check the logins on the box")
	o.add(t, "assistant", loginAsk)
	middle := "Nothing has landed yet. The validator recorded the box's state.\n\nNext: answer item 4 with \"keep\" or \"switch\"."
	o.add(t, "assistant", middle)
	last := "The implementer reports it's done; the validator is checking its work now.\n\nNext: answer item 4 with \"keep\" or \"switch\" while the validator runs."
	o.add(t, "assistant", last)
	got := o.waiting(t)
	if len(got) != 1 {
		t.Fatalf("waiting rows = %d, want one for the session", len(got))
	}
	if want := loginAsk + "\n\n" + middle + "\n\n" + last; got[0].Reply != want {
		t.Fatalf("row reply drops the referenced block or the latest message:\n%s", got[0].Reply)
	}
	if !strings.HasPrefix(got[0].Line, "4. Claude login on the box:") {
		t.Fatalf("row line = %q", got[0].Line)
	}
	o.add(t, "user", "keep")
	o.add(t, "assistant", "Kept the personal login. Nothing else is open.")
	if got := o.waiting(t); len(got) != 0 {
		t.Fatalf("an answered block still shows: %+v", got)
	}
}

func TestTenAskingTurnsInOneSessionShowOneRowWithTheLatestMessage(t *testing.T) {
	o := newOwnerSession(t)
	for i := 1; i <= 10; i++ {
		if i%2 == 1 {
			o.add(t, "user", fmt.Sprintf("turn %d", i))
		}
		msg := fmt.Sprintf("Turn %d done.\n\nOpen for you:\n%d. Ship step %d now?", i, i, i)
		o.add(t, "assistant", msg)
		got := o.waiting(t)
		if len(got) != 1 || got[0].Reply != msg {
			t.Fatalf("after turn %d: rows %+v, want one showing %q", i, got, msg)
		}
	}
}
