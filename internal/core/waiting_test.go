package core

import (
	"slices"
	"strings"
	"testing"
	"time"
)

const asking = "Done with the parser.\n\nOpen for you\n1. Ship it now?\n\nNext: watch CI.\n"

func firstAsk(reply string) string {
	_, block, ok := strings.Cut(reply, "Open for you\n")
	if !ok {
		return ""
	}
	line, _, _ := strings.Cut(block, "\n")
	return line
}

type sends struct{ sent []string }

func (l *sends) Dormant() []Dormant                            { return nil }
func (l *sends) Revive(string) (string, error)                 { return "", nil }
func (l *sends) Adopt(Live, func(string)) (Target, error)      { return Target{}, nil }
func (l *sends) Start(NewLaunch) (Started, error)              { return Started{}, nil }
func (l *sends) End(string, string) (string, error)            { return "", ErrNotFound }
func (l *sends) Weigh(string) (Toll, error)                    { return Toll{}, nil }
func (l *sends) GC() (string, error)                           { return "", nil }
func (l *sends) Relaunch(Live, string, string) (string, error) { return "", nil }
func (l *sends) Launched() []Launched                          { return nil }
func (l *sends) Send(s Live, text string) (string, error) {
	l.sent = append(l.sent, s.Key()+": "+text)
	return "sent", nil
}

func waitingWorld(t *testing.T) *world {
	w := newWorld(t)
	w.core.Ask = firstAsk
	return w
}

func waits(b Board) []string {
	var out []string
	for _, n := range b.Waiting {
		out = append(out, n.Session.Key()+" "+n.Line)
	}
	return out
}

func TestIdleSessionWhoseLastReplyAsksIsListedUnlessSomethingElseCoversIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		s       Session
		item    bool
		dismiss string
		want    bool
	}{
		{name: "asking idle turn", s: Session{Status: Idle, Facts: Facts{Result: asking}}, want: true},
		{name: "reply that asks nothing", s: Session{Status: Idle, Facts: Facts{Result: "All tests pass. Merged."}}},
		{name: "busy session", s: Session{Status: Busy, Facts: Facts{Result: asking}}},
		{name: "permission prompt", s: Session{Status: Waiting, WaitingFor: "permission prompt", Facts: Facts{Result: asking}}},
		{name: "prompt on screen", s: Session{Status: Idle, Prompt: &Prompt{Tool: "Bash"}, Facts: Facts{Result: asking}}},
		{name: "open item filed", s: Session{Status: Idle, Facts: Facts{Result: asking}}, item: true},
		{name: "dismissed this reply", s: Session{Status: Idle, Facts: Facts{Result: asking}}, dismiss: asking},
		{name: "dismissed an older reply", s: Session{Status: Idle, Facts: Facts{Result: asking}}, dismiss: "Open for you\n1. Older?", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := waitingWorld(t)
			w.run(10, 1, "/dev/t1", "claude", "s1", 0)
			tc.s.ID = "s1"
			w.claude.sessions[10] = tc.s
			if tc.item {
				if _, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "Ship it now?", Session: "s1"}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.dismiss != "" {
				if err := w.core.Store.DismissWaiting("s1", tc.dismiss); err != nil {
					t.Fatal(err)
				}
			}
			got := waits(w.board(t))
			if want := map[bool]int{true: 1, false: 0}[tc.want]; len(got) != want {
				t.Fatalf("waiting rows = %v, want %d", got, want)
			}
			if tc.want && got[0] != "s1 1. Ship it now?" {
				t.Errorf("row = %q, want the first question, not the reply's last line", got[0])
			}
		})
	}
}

func TestWaitingSessionsStayOffTheQueueLongestWaitingFirst(t *testing.T) {
	w := waitingWorld(t)
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	w.run(10, 1, "/dev/t1", "claude", "perm", 0)
	w.claude.sessions[10] = Session{ID: "perm", Status: Waiting, WaitingFor: "permission prompt"}
	w.run(20, 1, "/dev/t2", "claude", "late", 0)
	w.claude.sessions[20] = Session{ID: "late", Status: Idle, Since: at.Add(time.Hour), Facts: Facts{Result: asking}}
	w.run(30, 1, "/dev/t3", "claude", "early", 0)
	w.claude.sessions[30] = Session{ID: "early", Status: Idle, Since: at, Facts: Facts{Result: asking}}
	w.run(40, 1, "/dev/t4", "claude", "filer", 0)
	w.claude.sessions[40] = Session{ID: "filer", Status: Busy}
	if _, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "Pick a name?", Session: "filer"}); err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	var got []string
	for _, n := range b.Needs {
		switch {
		case n.Waiting():
			got = append(got, "wait "+n.Session.Key())
		case n.Item != nil:
			got = append(got, "item "+n.Item.Question)
		default:
			got = append(got, "perm "+n.Session.Key())
		}
	}
	if want := "perm perm|item Pick a name?"; strings.Join(got, "|") != want {
		t.Fatalf("needs = %v, want %s", got, want)
	}
	if got, want := waits(b), []string{"early 1. Ship it now?", "late 1. Ship it now?"}; !slices.Equal(got, want) {
		t.Fatalf("waiting = %v, want %v", got, want)
	}
}

func TestAnsweringAWaitingSessionSendsTheOwnersMessageAndClearsTheRowUntilItAsksAgain(t *testing.T) {
	w := waitingWorld(t)
	l := &sends{}
	w.core.Launches = l
	w.run(10, 1, "/dev/t1", "claude", "s1", 0)
	w.claude.sessions[10] = Session{ID: "s1", Status: Idle, Facts: Facts{Result: asking}}
	b := w.board(t)
	if len(b.Waiting) != 1 || len(b.Needs) != 0 {
		t.Fatalf("waiting = %+v, needs = %+v, want one waiting session and an empty queue", b.Waiting, b.Needs)
	}
	n := b.Waiting[0]
	if _, err := w.core.AnswerWaiting(*n.Session, n.Reply, "yes, ship it"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(l.sent, "|") != "s1: yes, ship it" {
		t.Errorf("sent = %v", l.sent)
	}
	if got := waits(w.board(t)); len(got) != 0 {
		t.Fatalf("answered row still shows: %v", got)
	}
	w.claude.sessions[10] = Session{ID: "s1", Status: Idle, Facts: Facts{Result: "Shipped.\n\nOpen for you\n1. Tag the release?\n\nNext: wait."}}
	if got := waits(w.board(t)); len(got) != 1 || got[0] != "s1 1. Tag the release?" {
		t.Fatalf("a new asking turn shows %v, want its row back", got)
	}
}
