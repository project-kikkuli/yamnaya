// Package drift tells, from a transcript alone and without a model, when a busy agent is going nowhere: the same failure over and
// over, its context compacted again and again with no work between, or writes landing in another repository.
package drift

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Kinds of sign.
const (
	Looping    = "looping"
	Compacting = "compacting"
	OffRepo    = "off-repo"
)

const (
	// Window is how many recent tool results a repeated failure is counted in.
	Window = 20
	// Repeats is how many of them must fail the same way.
	Repeats = 5
	// Span is how long the repeats must stretch over, so a burst the agent recovers from at once does not count.
	Span = 3 * time.Minute
	// Compactions is how many compactions in a row, each within Soon of the last with fewer than Between tool calls since, make a loop.
	Compactions = 3
	Between     = 10
	Soon        = 15 * time.Minute
)

// Sign is one way an agent is going nowhere, from Since until At.
type Sign struct {
	Kind     string
	Count    int
	Since    time.Time
	At       time.Time
	Evidence string
}

func (s Sign) String() string { return s.Kind + ": " + s.Evidence }

// Attempt is one tool result: Sig is "" when it succeeded.
type Attempt struct {
	Sig    string
	Detail string `json:",omitempty"`
	At     time.Time
}

// Tracker accumulates what a transcript shows, one record at a time, in a few bounded fields that persist in a scan cache.
type Tracker struct {
	Ring []Attempt `json:",omitempty"`
	// Batch is the assistant message the latest results answer, and BatchSigs the failures already counted for it.
	Batch        string   `json:",omitempty"`
	BatchSigs    []string `json:",omitempty"`
	Compacts     int      `json:",omitempty"`
	Calls        int      `json:",omitempty"`
	FirstCompact time.Time
	LastCompact  time.Time
	// Loop is the failure repeating now, from LoopSince to LoopAt, "" when none is; an episode lasts while it fills Repeats of the window.
	Loop       string `json:",omitempty"`
	LoopDetail string `json:",omitempty"`
	LoopSince  time.Time
	LoopAt     time.Time
}

// Call counts one tool call.
func (t *Tracker) Call() { t.Calls++ }

// Result records one tool result. Calls made together in one assistant message share batch, and a failure repeated within a
// batch counts once.
func (t *Tracker) Result(sig, detail, batch string, at time.Time) {
	if batch != t.Batch {
		t.Batch, t.BatchSigs = batch, nil
	}
	if sig != "" {
		for _, s := range t.BatchSigs {
			if s == sig {
				return
			}
		}
		t.BatchSigs = append(t.BatchSigs, sig)
	}
	t.Ring = append(t.Ring, Attempt{Sig: sig, Detail: detail, At: at})
	if len(t.Ring) > Window {
		t.Ring = append([]Attempt(nil), t.Ring[len(t.Ring)-Window:]...)
	}
	if n, _ := t.count(t.Loop); t.Loop != "" && n < Repeats {
		t.Loop = ""
	}
	if n, first := t.count(sig); t.Loop == "" && sig != "" && n >= Repeats && at.Sub(first) >= Span {
		t.Loop, t.LoopSince = sig, first
	}
	if sig != "" && sig == t.Loop {
		t.LoopDetail, t.LoopAt = detail, at
	}
}

// count is how many results in the window failed with sig, and when the first of them came.
func (t Tracker) count(sig string) (int, time.Time) {
	n := 0
	var first time.Time
	for _, a := range t.Ring {
		if a.Sig == sig {
			if n == 0 {
				first = a.At
			}
			n++
		}
	}
	return n, first
}

// Wrote records the agent changing a file: a failure that repeats after a change is iteration, not a loop.
func (t *Tracker) Wrote() {
	for i := range t.Ring {
		t.Ring[i] = Attempt{At: t.Ring[i].At}
	}
	t.Loop = ""
}

// Compacted records the harness compacting the agent's context.
func (t *Tracker) Compacted(at time.Time) {
	if t.Compacts > 0 && t.Calls < Between && at.Sub(t.LastCompact) < Soon {
		t.Compacts++
	} else {
		t.Compacts, t.FirstCompact = 1, at
	}
	t.Calls, t.LastCompact = 0, at
}

// Owner records a prompt the owner typed: what came before was steered.
func (t *Tracker) Owner() {
	t.Ring, t.Batch, t.BatchSigs, t.Loop = nil, "", nil, ""
}

// Signs are the ways the agent is going nowhere now.
func (t Tracker) Signs() []Sign {
	var out []Sign
	if t.Loop != "" {
		n, _ := t.count(t.Loop)
		out = append(out, Sign{Kind: Looping, Count: n, Since: t.LoopSince, At: t.LoopAt,
			Evidence: fmt.Sprintf("failed the same way %d times in %s: %s", n, short(t.LoopAt.Sub(t.LoopSince)), t.LoopDetail)})
	}
	if t.Compacts >= Compactions && t.Calls < Between {
		out = append(out, Sign{Kind: Compacting, Count: t.Compacts, Since: t.FirstCompact, At: t.LastCompact,
			Evidence: fmt.Sprintf("context compacted %d times in %s with under %d tool calls between any two", t.Compacts, short(t.LastCompact.Sub(t.FirstCompact)), Between)})
	}
	return out
}

func short(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

var (
	hexRun  = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	digits  = regexp.MustCompile(`\d+`)
	tmpPath = regexp.MustCompile(`/(private/)?(tmp|var/folders)/\S*`)
	bare    = regexp.MustCompile(`^(Exit code N|exit status N|Error: Exit code N)?$`)
	// waits are sleeps an agent puts before a retry, which leave the command the same.
	waits = regexp.MustCompile(`^(sleep N\s*(;|&&)\s*)+`)
	// polls are commands whose failure means "not yet", such as CI checks still pending.
	polls = regexp.MustCompile(`gh pr checks|gh run watch`)
	// limits are the harness or its API refusing for capacity, which the agent waits out rather than causes.
	limits = regexp.MustCompile(`hit your (session|weekly|usage) limit|temporarily unavailable \(rate-limited\)|temporarily limiting requests`)
	// searches exit nonzero with no output when nothing matches, which is an answer, not a failure.
	searches = regexp.MustCompile(`(^|&&|;|\||\s)(rg|grep|egrep|fgrep)\s`)
)

// Norm reduces text to what stays the same across repeats of one failure: ids, numbers and temporary paths dropped, space collapsed.
func Norm(s string) string {
	s = hexRun.ReplaceAllString(s, "H")
	s = digits.ReplaceAllString(s, "N")
	s = tmpPath.ReplaceAllString(s, "/tmp/X")
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 100 {
		s = string(r[:100])
	}
	return s
}

// Failure is the signature and the readable detail of a failed tool call; sig is "" for a call whose failure is only a poll's "not yet"
// or a capacity limit.
// The signature is the failure's text, so one blocker hit through different tools repeats; a failure with no text of its own is
// told apart by its tool and command.
func Failure(tool, command, text string) (sig, detail string) {
	if polls.MatchString(command) || limits.MatchString(text) {
		return "", ""
	}
	sig = Norm(text)
	if bare.MatchString(sig) && searches.MatchString(command) {
		return "", ""
	}
	if bare.MatchString(sig) {
		sig = tool + ": " + sig + " $ " + waits.ReplaceAllString(Norm(command), "")
	}
	detail = tool + ": " + clip(text, 120)
	if c := clip(command, 80); c != "" {
		detail += " ($ " + c + ")"
	}
	return sig, detail
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n]) + "…"
	}
	return s
}

// Episodes collects each sign once per episode while a transcript is replayed: See takes the signs after every record, and a sign
// still running keeps its latest count and evidence.
type Episodes struct {
	list []Sign
	at   map[string]int
}

func (e *Episodes) See(signs []Sign) {
	if e.at == nil {
		e.at = map[string]int{}
	}
	for _, s := range signs {
		k := s.Kind + "\x00" + s.Since.String()
		if i, ok := e.at[k]; ok {
			e.list[i] = s
			continue
		}
		e.at[k] = len(e.list)
		e.list = append(e.list, s)
	}
}

func (e Episodes) List() []Sign { return e.list }
