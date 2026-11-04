package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

// wireWorld is the real thing but the native binary: Wire, the real wrapper and adapter, the real Meter, a private tmux, and a fake claude
// that prints whatever screen its login's directory holds and keeps a real session file. The clock is the only other thing it steers.
type wireWorld struct {
	c                *core.Core
	l                launcher
	state, cwd, log  string
	def, work, other profile.Profile
	rec              launch.Record
	clock            *time.Time
}

const wireIdle = "✻ Done\n────────────────\n❯ \n────────────────\n"

const wireClaude = `#!/bin/bash
case " $* " in *" auth status "*) [ -f "$CLAUDE_CONFIG_DIR/hang" ] && sleep 30; printf '%s\n' '{"loggedIn":true,"authMethod":"claude.ai"}'; exit 0;; esac
root="$CLAUDE_CONFIG_DIR"
prev=""; for a in "$@"; do [ "$prev" = --resume ] && id="$a"; [ "$prev" = --session-id ] && id="$a"; prev="$a"; done
printf '%s\n' "$YAMNAYA_PROFILE" >> "$WIRE_LOG"
mkdir -p "$root/sessions"
printf '{"pid":%d,"sessionId":"%s","cwd":"%s","status":"%s","statusUpdatedAt":%d}' $$ "$id" "$PWD" "$(cat "$root/status")" "$(date +%s%3N)" > "$root/sessions/$$.json"
[ -f "$root/child" ] && { setsid bash -c 'exec -a claude sleep 100000' </dev/null >/dev/null 2>&1 & }
clear
cat "$root/screen.txt"
exec -a claude sleep 100000
`

func wireTurn(id string, at time.Time) string {
	return fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}
{"type":"assistant","sessionId":"%s","timestamp":"%s","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"FINISHED"}]}}
`, id, at.Add(-time.Minute).UTC().Format(time.RFC3339Nano), id, at.UTC().Format(time.RFC3339Nano))
}

func newWireWorld(t *testing.T) *wireWorld {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	state, bins := t.TempDir(), t.TempDir()
	state, _ = filepath.EvalSymlinks(state)
	cwd := filepath.Join(state, "work")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("YAMNAYA_YM_SOCKET", tmuxtest.Socket(t))
	t.Setenv("YAMNAYA_TMUX_SOCKET", tmuxtest.Socket(t))
	t.Setenv("WIRE_LOG", filepath.Join(state, "starts"))
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	for _, k := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "OPENAI_API_KEY", "CODEX_API_KEY", "CODEX_ACCESS_TOKEN"} {
		t.Setenv(k, "")
	}
	t.Setenv(profile.EnvKey("claude"), t.TempDir())
	t.Setenv(profile.EnvKey("codex"), t.TempDir())
	if err := os.WriteFile(filepath.Join(bins, "claude"), []byte(wireClaude), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claude-work", "personal"} {
		p := profile.Profile{Harness: "claude", ID: id, Root: t.TempDir(), Explicit: true}
		if err := profile.Add(state, p); err != nil {
			t.Fatal(err)
		}
		p.Root = physicalProfileRoot(p.Root)
		subscriptionIdentity(t, &p)
	}
	ps, err := profile.Load(state)
	if err != nil {
		t.Fatal(err)
	}
	find := func(id string) profile.Profile {
		p, err := profile.Find(ps, "claude", id)
		if err != nil {
			t.Fatal(err)
		}
		subscriptionIdentity(t, &p)
		return p
	}
	w := &wireWorld{state: state, cwd: cwd, log: filepath.Join(state, "starts"), def: find("default"), work: find("claude-work"), other: find("personal")}
	c, err := Wire(filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Store.Close() })
	m := usage.New(state)
	t.Cleanup(func() { m.Close() })
	c.Meter = m
	now := time.Now()
	w.clock = &now
	c.Now = func() time.Time { return *w.clock }
	w.c, w.l = c, c.Launches.(launcher)
	for _, p := range []profile.Profile{w.def, w.work, w.other} {
		w.screen(t, p, wireIdle, "idle")
	}
	w.rec = launch.Record{Key: "conversation", Harness: "claude", Session: profileConversation, Worktree: launch.Worktree{Path: cwd}}
	setRecordProfile(&w.rec, w.def)
	if err = w.l.records.Save(w.rec); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(w.def.Root, "projects", strings.NewReplacer("/", "-", ".", "-").Replace(cwd), profileConversation+".jsonl")
	putProfileFile(t, transcript, wireTurn(profileConversation, now.Add(-time.Minute)))
	return w
}

// screen is what the login's sessions print from their next landing on.
func (w *wireWorld) screen(t *testing.T, p profile.Profile, text, status string) {
	t.Helper()
	putProfileFile(t, filepath.Join(p.Root, "screen.txt"), text)
	putProfileFile(t, filepath.Join(p.Root, "status"), status)
}

func wireScreen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "harness", "claude", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// land starts the conversation on its login, past any gate, as a session that was already there.
func (w *wireWorld) land(t *testing.T) {
	t.Helper()
	once := w.l
	once.anyway = true
	rec, _ := w.l.records.Find(w.rec.Key)
	if _, err := once.revive(rec); err != nil {
		t.Fatal(err)
	}
	w.settle(t, 1)
}

// settle waits for the fake to have started want times and for that landing's session to be on the board with its screen printed.
func (w *wireWorld) settle(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if len(w.starts(t)) >= want {
			b, err := w.c.Board()
			if err != nil {
				t.Fatal(err)
			}
			if s, ok := b.Session(profileConversation); ok && s.PID != 0 && s.TTY != "" {
				if rec, found := w.l.records.Find(w.rec.Key); found && profile.ID(s.Profile) == profile.ID(rec.Profile) {
					time.Sleep(100 * time.Millisecond)
					return
				}
			}
		}
		if time.Now().After(deadline) {
			b, _ := w.c.Board()
			t.Fatalf("no landing %d: starts %q, sessions %+v", want, w.starts(t), b.Sessions)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (w *wireWorld) starts(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(w.log)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(string(b))
}

// pass is one serve tick step later, waiting out any landing it caused.
func (w *wireWorld) pass(t *testing.T, step time.Duration) {
	t.Helper()
	*w.clock = w.clock.Add(step)
	before := len(w.starts(t))
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	if err = w.l.RecoverProfiles(b); err != nil {
		t.Fatal(err)
	}
	if err = w.l.Failover(b); err != nil {
		t.Fatal(err)
	}
	if b, err = w.c.Board(); err != nil {
		t.Fatal(err)
	}
	if _, alive := b.Session(profileConversation); len(w.starts(t)) > before || !alive {
		w.settle(t, before+1)
	}
}

// row writes the login's one limit file, as its statusline does: one Seen for all its windows.
func (w *wireWorld) row(t *testing.T, p profile.Profile, fiveHour, sevenDay float64, reset time.Time) {
	t.Helper()
	window := func(used float64) map[string]any {
		return map[string]any{"used_percentage": used, "resets_at": reset.Unix()}
	}
	doc := map[string]any{"account": p.Account, "profile": p.ID, "seen": w.clock.Unix() + 1}
	if fiveHour >= 0 {
		doc["five_hour"] = window(fiveHour)
	}
	if sevenDay >= 0 {
		doc["seven_day"] = window(sevenDay)
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	name := "claude-limits.json"
	if p.ID != "default" {
		name = "claude-limits." + p.ID + ".json"
	}
	if err = os.WriteFile(filepath.Join(w.state, name), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// exhaust leaves the logins with no usage, far off, so the session has nowhere to go.
func (w *wireWorld) exhaust(t *testing.T, ps ...profile.Profile) {
	t.Helper()
	for _, p := range ps {
		w.row(t, p, -1, 100, w.clock.Add(9*24*time.Hour))
	}
}

func (w *wireWorld) rowText(t *testing.T) string {
	t.Helper()
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, ok := b.Session(profileConversation)
	if !ok || s.Switching == nil {
		return ""
	}
	return s.Switching.String()
}

func (w *wireWorld) items(t *testing.T) []core.Item {
	t.Helper()
	items, err := w.c.Store.Items(core.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func (w *wireWorld) bar(t *testing.T, p profile.Profile) loginBar {
	t.Helper()
	b, err := readBar(barPath(w.state, p))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wireRewrap(text string, width int) string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		r := []rune(line)
		for len(r) > width {
			out = append(out, string(r[:width]))
			r = r[width:]
		}
		out = append(out, string(r))
	}
	return strings.Join(out, "\n")
}

func TestWireEveryLoginAtADialogMovesOnThenStaysAndSaysHowToGetOut(t *testing.T) {
	for _, c := range []struct {
		name   string
		screen func(string) string
	}{
		{name: "full width", screen: func(s string) string { return s }},
		{name: "wrapped at 30", screen: func(s string) string { return wireRewrap(s, 30) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			dialog := c.screen(wireScreen(t, "silent-quota-dialog.txt"))
			for _, p := range []profile.Profile{w.def, w.work, w.other} {
				w.screen(t, p, dialog, "waiting")
			}
			w.land(t)
			for range 12 {
				w.pass(t, time.Minute)
			}
			if got := strings.Join(w.starts(t), " "); got != "default claude-work personal" {
				t.Fatalf("landings %q", got)
			}
			row := w.rowText(t)
			if !strings.Contains(row, "stopped at its limit, no usable login, tries this login again ") || !strings.Contains(row, "ym switch "+core.Short(profileConversation)+" --profile") {
				t.Fatalf("row %q", row)
			}
			if items := w.items(t); len(items) != 3 {
				t.Fatalf("%d items, want one per login: %+v", len(items), items)
			}
		})
	}
}

// With rows refreshing after every landing, a stop's bar lifts once. The stop that returns holds the login until its time is up.
func TestWireARowBelowEvidenceRefreshedEveryPassCannotLoopARestart(t *testing.T) {
	for _, c := range []struct {
		name  string
		five  float64
		seven float64
		lifts int
	}{
		{name: "89%", five: -1, seven: 89, lifts: 1},
		{name: "20%", five: -1, seven: 20, lifts: 1},
		{name: "91% is no evidence", five: -1, seven: 91},
		{name: "one window low, one at its limit", five: 40, seven: 100},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
			w.row(t, w.def, c.five, c.seven, w.clock.Add(24*time.Hour))
			w.land(t)
			want := 1 + c.lifts
			for range 30 {
				w.pass(t, time.Minute)
				w.row(t, w.def, c.five, c.seven, w.clock.Add(24*time.Hour))
			}
			if got := len(w.starts(t)); got != want {
				t.Fatalf("%d landings in 30 passes, want %d: bar %+v", got, want, w.bar(t, w.def))
			}
			if c.seven == 100 {
				return
			}
			*w.clock = w.bar(t, w.def).Until
			w.pass(t, time.Minute)
			if got := len(w.starts(t)); got != want+1 {
				t.Fatalf("%d landings past the bar, want %d: %+v", got, want+1, w.bar(t, w.def))
			}
			for range 30 {
				w.pass(t, time.Minute)
				w.row(t, w.def, c.five, c.seven, w.clock.Add(24*time.Hour))
			}
			if got := len(w.starts(t)); got != want+1 {
				t.Fatalf("%d landings in the next 30 passes, want %d: %+v", got, want+1, w.bar(t, w.def))
			}
		})
	}
}

func TestWireAnUnknownResetIsReleasedAtFiveHoursForOneAttempt(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	stopped := *w.clock
	bar := w.bar(t, w.def)
	if !bar.Until.Equal(stopped.Add(5*time.Hour)) || bar.Known {
		t.Fatalf("bar %+v, stopped %v", bar, stopped)
	}
	row := w.rowText(t)
	if !strings.Contains(row, "tries this login again") {
		t.Fatalf("row %q", row)
	}
	first := w.items(t)
	if len(first) != 1 || !strings.Contains(first[0].Tried, "tries this login again") {
		t.Fatalf("items %+v", first)
	}
	for *w.clock = stopped.Add(4 * time.Hour); w.clock.Before(stopped.Add(5*time.Hour - 6*time.Minute)); {
		w.pass(t, 5*time.Minute)
	}
	if got := len(w.starts(t)); got != 1 {
		t.Fatalf("%d landings inside the bar", got)
	}
	*w.clock = stopped.Add(5 * time.Hour)
	w.pass(t, time.Minute)
	if got := len(w.starts(t)); got != 2 {
		t.Fatalf("%d landings after the bar, want the one attempt", got)
	}
	w.pass(t, time.Minute)
	next := w.bar(t, w.def)
	if !next.Until.After(stopped.Add(5 * time.Hour)) {
		t.Fatalf("the dialog coming back set no new bar: %+v", next)
	}
	for range 20 {
		w.pass(t, time.Minute)
	}
	if got := len(w.starts(t)); got != 2 {
		t.Fatalf("%d landings after a new bar", got)
	}
	if items := w.items(t); len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
}

func TestWireAFalseStopIsLiftedByANewerRowForEverySession(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.other)
	w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	rec, _ := w.l.records.Find(w.rec.Key)
	if rec.Profile != "claude-work" {
		t.Fatalf("session on %q", rec.Profile)
	}
	if !w.bar(t, w.def).Until.After(*w.clock) || len(w.items(t)) != 1 {
		t.Fatalf("no bar or item: %+v %+v", w.bar(t, w.def), w.items(t))
	}
	w.row(t, w.def, 40, 40, w.clock.Add(24*time.Hour))
	w.pass(t, time.Minute)
	if !barredUntil(w.state, w.def, w.l.limits(), *w.clock).IsZero() {
		t.Fatalf("a newer row left the login barred: %+v", w.bar(t, w.def))
	}
	if it := w.items(t)[0]; it.State != core.ItemDismissed {
		t.Fatalf("the item outlived the bar: %+v", it)
	}
	w.screen(t, w.work, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
	w.screen(t, w.def, wireIdle, "idle")
	w.exhaust(t, w.work)
	once := w.l
	once.operation = profileConversation
	b, _ := w.c.Board()
	s, _ := b.Session(profileConversation)
	if _, err := w.c.Stop(s); err != nil {
		t.Fatal(err)
	}
	rec, _ = w.l.records.Find(w.rec.Key)
	if _, err := once.revive(rec); err != nil {
		t.Fatal(err)
	}
	w.settle(t, 3)
	w.pass(t, time.Minute)
	if rec, _ = w.l.records.Find(w.rec.Key); rec.Profile != "default" {
		t.Fatalf("session on %q, want the login the row cleared", rec.Profile)
	}
}

func TestWireAResetLearnedLaterUpdatesTheBarAndItsOneItem(t *testing.T) {
	t.Run("a row's reset, then that reset passes with no newer row", func(t *testing.T) {
		w := newWireWorld(t)
		w.exhaust(t, w.work, w.other)
		w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
		w.land(t)
		w.pass(t, time.Minute)
		if it := w.items(t); len(it) != 1 || !strings.Contains(it[0].Question, "not yet known") {
			t.Fatalf("items %+v", it)
		}
		reset := w.clock.Add(8 * time.Hour).Truncate(time.Second)
		w.row(t, w.def, -1, 100, reset)
		w.pass(t, time.Minute)
		items := w.items(t)
		if len(items) != 1 || !strings.Contains(items[0].Question, reset.UTC().Format("Jan 2 15:04 UTC")) || strings.Contains(items[0].Question, "not yet known") {
			t.Fatalf("items %+v", items)
		}
		bar := w.bar(t, w.def)
		if !bar.Until.Equal(reset) || !bar.Known {
			t.Fatalf("bar %+v, reset %v", bar, reset)
		}
		if row := w.rowText(t); !strings.Contains(row, "resets "+quotaReset(reset)) {
			t.Fatalf("row %q", row)
		}
		later := reset.Add(48 * time.Hour)
		w.row(t, w.def, -1, 100, later)
		w.pass(t, time.Minute)
		items = w.items(t)
		if len(items) != 1 || !strings.Contains(items[0].Question, later.UTC().Format("Jan 2 15:04 UTC")) {
			t.Fatalf("a later window did not restate the item: %+v", items)
		}
		if bar = w.bar(t, w.def); !bar.Until.Equal(later) {
			t.Fatalf("bar %+v, later %v", bar, later)
		}
		*w.clock = later
		w.pass(t, time.Minute)
		if got := len(w.starts(t)); got != 2 {
			t.Fatalf("%d landings once the reset passed with no newer row", got)
		}
	})
	t.Run("a window refreshing beside the one at its limit files nothing more", func(t *testing.T) {
		w := newWireWorld(t)
		w.exhaust(t, w.work, w.other)
		w.land(t)
		reset := w.clock.Add(9 * 24 * time.Hour)
		for range 6 {
			w.row(t, w.def, 40, 100, reset)
			w.pass(t, time.Minute)
		}
		if items := w.items(t); len(items) != 1 || items[0].ID != "D1" {
			t.Fatalf("items %+v", items)
		}
	})
	t.Run("a stranded login's item is never re-filed however long it waits", func(t *testing.T) {
		w := newWireWorld(t)
		w.exhaust(t, w.work, w.other, w.def)
		w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
		w.land(t)
		w.pass(t, time.Minute)
		first := w.items(t)
		w.exhaust(t, w.def)
		for range 10 {
			w.pass(t, 20*time.Hour)
			if w.clock.After(w.bar(t, w.def).Until.Add(-24 * time.Hour)) {
				break
			}
		}
		items := w.items(t)
		if len(first) != 1 || len(items) != 1 || items[0].Seq != first[0].Seq {
			t.Fatalf("items %+v after %+v", items, first)
		}
	})
}

func TestWireAFailedStopWhileContinuingIsTriedAgain(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	fails := 1
	w.c.Signal = func(pid int, sig syscall.Signal) error {
		if fails > 0 {
			fails--
			return fmt.Errorf("signal failed")
		}
		return syscall.Kill(pid, sig)
	}
	stop := w.bar(t, w.def)
	*w.clock = stop.Until
	w.pass(t, time.Minute)
	if got := len(w.starts(t)); got != 1 || fails != 0 {
		t.Fatalf("%d landings with the stop failed, fails left %d", got, fails)
	}
	if after := w.bar(t, w.def); !after.Until.Equal(stop.Until) {
		t.Fatalf("a failed stop changed the bar: %+v", after)
	}
	w.pass(t, 2*time.Minute)
	if got := len(w.starts(t)); got != 2 {
		t.Fatalf("%d landings after the stop could work", got)
	}
}

func TestWireASwitchToAutoSkipsALoginAnotherSessionBarred(t *testing.T) {
	w := newWireWorld(t)
	w.land(t)
	if err := recordStop(w.state, w.work, *w.clock, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.l.QueueProfileSwitch(w.rec.Key, "auto", "testing"); err != nil {
		t.Fatal(err)
	}
	if m, err := loadMigration(w.state, "claude", profileConversation); err != nil || m.Destination.ID != "personal" {
		t.Fatalf("auto chose %+v, %v", m.Destination, err)
	}
}

func TestWireAHealthyIdleSessionOnABarredLoginStaysPut(t *testing.T) {
	w := newWireWorld(t)
	w.land(t)
	w.exhaust(t, w.def)
	for range 5 {
		w.pass(t, time.Minute)
	}
	rec, _ := w.l.records.Find(w.rec.Key)
	if got := len(w.starts(t)); got != 1 || rec.Profile != "default" {
		t.Fatalf("%d landings, session on %q", got, rec.Profile)
	}
	if items := w.items(t); len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
	if err := recordStop(w.state, w.def, *w.clock, time.Time{}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		w.pass(t, time.Minute)
	}
	if got := len(w.starts(t)); got != 1 {
		t.Fatalf("another session's stop moved an idle one: %d landings", got)
	}
}

func TestWireExplicitMovesReadTheBarAsAWarningAndABypass(t *testing.T) {
	w := newWireWorld(t)
	w.land(t)
	if err := recordStop(w.state, w.work, *w.clock, time.Time{}); err != nil {
		t.Fatal(err)
	}
	out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "testing")
	if err != nil || !strings.Contains(out.Message, "out of its subscription limit") {
		t.Fatalf("switch %+v, %v", out, err)
	}
	if err = recordStop(w.state, w.def, *w.clock, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err = recordStop(w.state, w.other, *w.clock, time.Time{}); err != nil {
		t.Fatal(err)
	}
	refused := w.l.refusal(w.rec)
	var wait *core.QuotaWait
	if !errors.As(refused, &wait) || !strings.Contains(refused.Error(), "ym switch") || !strings.Contains(refused.Error(), "once it is stopped") {
		t.Fatalf("refusal %v", refused)
	}
	bypass := w.l
	bypass.anyway = true
	rec, _ := w.l.records.Find(w.rec.Key)
	if _, err = bypass.revive(rec); err == nil || !strings.Contains(err.Error(), "is running") {
		t.Fatalf("--anyway on a live session: %v", err)
	}
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := b.Session(profileConversation)
	if _, err = w.c.Stop(s); err != nil {
		t.Fatal(err)
	}
	before := len(w.starts(t))
	if _, err = bypass.revive(rec); err != nil {
		t.Fatal(err)
	}
	w.settle(t, before+1)
	if got := w.bar(t, w.def); got.Until.IsZero() {
		t.Fatalf("a bypass cleared the bar: %+v", got)
	}
}

func TestWireAStopThatCannotBeProvedSaysWhyAndBarsTheLogin(t *testing.T) {
	quota := func(at time.Time) string {
		return fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}
{"type":"assistant","sessionId":"%s","timestamp":"%s","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"You've hit your limit"}]}}
`, profileConversation, at.Add(-time.Minute).UTC().Format(time.RFC3339Nano), profileConversation, at.UTC().Format(time.RFC3339Nano))
	}
	hitLimit := "  ⎿  You've hit your weekly limit · resets Nov 5, 3am\n\nWhat do you want to do?\n❯ 1. Stop and wait for limit to reset\n  2. Wait here, then continue automatically at Nov 5, 3am\n  3. Switch to usage credits\n\nEnter to confirm · Esc to cancel\n"
	for _, c := range []struct {
		name        string
		screen      string
		child       bool
		turn        string
		wantRowPart string
	}{
		{name: "a silent dialog whose turn has not ended", screen: wireScreen(t, "silent-quota-dialog.txt"), turn: "user", wantRowPart: "has not ended"},
		{name: "a hit-limit dialog with a busy child, rows low", screen: hitLimit, child: true, turn: "quota", wantRowPart: "is active"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			path := filepath.Join(w.def.Root, "projects", strings.NewReplacer("/", "-", ".", "-").Replace(w.cwd), profileConversation+".jsonl")
			switch c.turn {
			case "user":
				putProfileFile(t, path, fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}`+"\n", profileConversation, w.clock.UTC().Format(time.RFC3339Nano)))
			case "quota":
				putProfileFile(t, path, quota(w.clock.Add(-time.Minute)))
			}
			if c.child {
				putProfileFile(t, filepath.Join(w.def.Root, "child"), "")
			}
			w.row(t, w.def, -1, 50, w.clock.Add(24*time.Hour))
			w.screen(t, w.def, c.screen, "waiting")
			w.land(t)
			if _, err := w.c.Meter.(*usage.Meter).Scan(time.Now()); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				w.pass(t, time.Minute)
			}
			if row := w.rowText(t); !strings.HasPrefix(row, "stopped at its limit: ") || !strings.Contains(row, c.wantRowPart) {
				t.Fatalf("row %q", row)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" || len(w.starts(t)) != 1 {
				t.Fatalf("moved: %q, starts %q", rec.Profile, w.starts(t))
			}
			if items := w.items(t); len(items) != 1 {
				t.Fatalf("items %+v", items)
			}
		})
	}
}

func TestWireTheFooterIsAStopAndClaudeIsLeftToContinue(t *testing.T) {
	footer := wireScreen(t, "quota-footer.txt")
	t.Run("a usable login takes it whatever rows say", func(t *testing.T) {
		w := newWireWorld(t)
		w.row(t, w.def, -1, 50, w.clock.Add(24*time.Hour))
		w.screen(t, w.def, footer, "idle")
		w.land(t)
		w.pass(t, time.Minute)
		if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" {
			t.Fatalf("session on %q", rec.Profile)
		}
	})
	t.Run("nowhere to go, then past its reset", func(t *testing.T) {
		w := newWireWorld(t)
		w.exhaust(t, w.work, w.other)
		w.row(t, w.def, -1, 50, w.clock.Add(24*time.Hour))
		w.screen(t, w.def, footer, "idle")
		w.land(t)
		w.pass(t, time.Minute)
		bar := w.bar(t, w.def)
		if !bar.Known || bar.Until.Hour() != 3 || bar.Until.Before(*w.clock) {
			t.Fatalf("bar %+v", bar)
		}
		if row := w.rowText(t); !strings.Contains(row, "no usable login, resets ") {
			t.Fatalf("row %q", row)
		}
		w.row(t, w.def, -1, 50, w.clock.Add(24*time.Hour))
		for range 5 {
			w.pass(t, time.Minute)
		}
		if got := len(w.starts(t)); got != 1 {
			t.Fatalf("%d landings over a footer inside its bar", got)
		}
		*w.clock = bar.Until
		w.pass(t, time.Minute)
		if got := len(w.starts(t)); got != 1 {
			t.Fatalf("restarted on top of Claude's own continue: %d landings", got)
		}
		w.pass(t, selfContinueGrace)
		if got := len(w.starts(t)); got != 2 {
			t.Fatalf("%d landings with a footer up past its grace", got)
		}
	})
}

func TestWireAHungStatusCheckHoldsNoCycleAndNoItemCallsItSignedOut(t *testing.T) {
	w := newWireWorld(t)
	w.screen(t, w.def, wireScreen(t, "silent-quota-dialog.txt"), "waiting")
	w.land(t)
	for _, p := range []profile.Profile{w.work, w.other} {
		putProfileFile(t, filepath.Join(p.Root, "hang"), "")
	}
	started := time.Now()
	w.pass(t, time.Minute)
	if took := time.Since(started); took > 8*time.Second {
		t.Fatalf("a pass took %v on hung checks", took)
	}
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
		t.Fatalf("session on %q", rec.Profile)
	}
	if row := w.rowText(t); !strings.Contains(row, "has not answered") {
		t.Fatalf("row %q", row)
	}
	items := w.items(t)
	if len(items) != 1 || !strings.HasPrefix(items[0].Rec, "switch to ") {
		t.Fatalf("the item calls a hung login gone: %+v", items)
	}
	for _, p := range []profile.Profile{w.work, w.other} {
		if err := os.Remove(filepath.Join(p.Root, "hang")); err != nil {
			t.Fatal(err)
		}
	}
	w.pass(t, 2*time.Minute)
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile == "default" {
		t.Fatal("a login answering again did not take the session")
	}
}

func TestLoginRecordKeepsEveryConcurrentWrite(t *testing.T) {
	dir := t.TempDir()
	p := profile.Profile{Harness: "claude", ID: "default", Account: "claude/u/a"}
	var wg sync.WaitGroup
	base := time.Unix(1000, 0)
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := updateBar(dir, p, func(b *loginBar) error {
				if b.Stopped.IsZero() {
					b.Stopped = base
				}
				b.Stopped = b.Stopped.Add(time.Second)
				return nil
			}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	b, err := readBar(barPath(dir, p))
	if err != nil || !b.Stopped.Equal(base.Add(40*time.Second)) {
		t.Fatalf("%+v, %v", b, err)
	}
}

func TestABarredOtherHarnessCannotBePortedOnto(t *testing.T) {
	w := newWireWorld(t)
	ps, err := profile.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	var cx profile.Profile
	for _, p := range ps {
		if p.Harness == "codex" {
			cx = p
		}
	}
	subscriptionIdentity(t, &cx)
	now := *w.clock
	for _, p := range []profile.Profile{w.def, w.work, w.other} {
		if err = recordStop(w.state, p, now, time.Time{}); err != nil {
			t.Fatal(err)
		}
	}
	dest := func() (profile.Profile, error) {
		ps, _ := profile.Load(w.state)
		return failoverDestination(usableLogins(w.state, failoverAttempt{}, ps, nil, now), core.Exhaustion{Harness: "claude", Account: w.def.Account}, nil, now, w.cwd)
	}
	if got, err := dest(); err != nil || got.Harness != "codex" {
		t.Fatalf("every claude login barred: %+v, %v", got, err)
	}
	if err = recordStop(w.state, cx, now, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if got, err := dest(); err == nil {
		t.Fatalf("every login of both harnesses barred, yet ported to %+v", got)
	}
}
