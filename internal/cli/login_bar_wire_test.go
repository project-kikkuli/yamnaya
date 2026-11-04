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
	"github.com/project-kikkuli/yamnaya/internal/proc"
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
	landed           bool
	meter            *usage.Meter
}

const wireIdle = "✻ Done\n────────────────\n❯ \n────────────────\n"

const wireClaude = `#!/bin/bash
case " $* " in *" auth status "*|*" login status "*) echo x >> "${CLAUDE_CONFIG_DIR:-$HOME/.claude}/status-calls"; while [ -f "$CLAUDE_CONFIG_DIR/hang" ]; do sleep 0.2; done; [ -f "$CLAUDE_CONFIG_DIR/five" ] && sleep 5; [ -f "$CLAUDE_CONFIG_DIR/slow" ] && { rm "$CLAUDE_CONFIG_DIR/slow"; sleep 3; }; [ -f "$CLAUDE_CONFIG_DIR/fail" ] && exit 3; [ -f "$CLAUDE_CONFIG_DIR/killed" ] && kill -9 $$; [ -f "$CLAUDE_CONFIG_DIR/out" ] && { printf '%s\n' '{"loggedIn":false,"authMethod":"none"}'; exit 1; }; printf '%s\n' '{"loggedIn":true,"authMethod":"claude.ai"}'; exit 0;; esac
root="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
prev=""; for a in "$@"; do [ "$prev" = --resume ] && id="$a"; [ "$prev" = --session-id ] && id="$a"; prev="$a"; done
printf '%s\n' "$YAMNAYA_PROFILE" >> "$WIRE_LOG"
printf '%s %s %s\n' "$YAMNAYA_PROFILE" "$CLAUDE_SECURESTORAGE_CONFIG_DIR" "$root" >> "$WIRE_LOG.env"
mkdir -p "$root/sessions"
printf '{"pid":%d,"sessionId":"%s","cwd":"%s","status":"%s","statusUpdatedAt":%d}' $$ "$id" "$PWD" "$(cat "$root/status")" "$(date +%s%3N)" > "$root/sessions/$$.json"
[ -f "$root/child" ] && { setsid bash -c 'exec -a claude sleep 60' </dev/null >/dev/null 2>&1 & }
state() { printf '{"pid":%d,"sessionId":"%s","cwd":"%s","status":"%s","statusUpdatedAt":%d}' $main "$id" "$PWD" "$(cat "$root/status")" "$(date +%s%3N)" > "$root/sessions/$main.json.tmp" && mv "$root/sessions/$main.json.tmp" "$root/sessions/$main.json"; }
main=$$
( last=$(stat -c %y "$root/screen.txt"); while kill -0 $main 2>/dev/null; do now=$(stat -c %y "$root/screen.txt"); if [ "$now" != "$last" ]; then last=$now; state; clear; cat "$root/screen.txt"; fi; sleep 0.1; done ) </dev/null &
clear
cat "$root/screen.txt"
exec -a claude sleep 100000
`

func wireTurn(id string, at time.Time) string {
	return fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}
{"type":"assistant","sessionId":"%s","timestamp":"%s","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"FINISHED"}]}}
`, id, at.Add(-time.Minute).UTC().Format(time.RFC3339Nano), id, at.UTC().Format(time.RFC3339Nano))
}

func newWireWorld(t *testing.T, inherited ...string) *wireWorld {
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
	if len(inherited) > 0 {
		for _, k := range []string{"CLAUDE_SECURESTORAGE_CONFIG_DIR", "YAMNAYA_PROFILE"} {
			os.Unsetenv(k)
		}
		t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", inherited[0])
		os.Unsetenv(profile.EnvKey("claude"))
		t.Setenv("HOME", state)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bins, name), []byte(wireClaude), 0o700); err != nil {
			t.Fatal(err)
		}
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
	w.meter = m
	if _, err = m.Scan(time.Now()); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	w.clock = &now
	c.Now = func() time.Time { return *w.clock }
	w.l = c.Launches.(launcher)
	w.l.statuses = &statusChecks{}
	c.Launches = w.l
	w.c = c
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

// wireDialog is the real silent dialog with a reset it does not state, the only way a stop's reset is unknown.
func wireDialog(t *testing.T) string {
	t.Helper()
	return strings.ReplaceAll(wireScreen(t, "silent-quota-dialog.txt"), "Nov 5, 3am", "the reset")
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
	w.landed = true
	w.settle(t, len(w.starts(t)))
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
	if _, alive := b.Session(profileConversation); len(w.starts(t)) > before || w.landed && !alive {
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
			dialog := c.screen(wireDialog(t))
			for _, p := range []profile.Profile{w.def, w.work, w.other} {
				w.screen(t, p, dialog, "waiting")
			}
			w.land(t)
			for range 6 {
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
		{name: "89%", five: -1, seven: 89},
		{name: "91% is no evidence", five: -1, seven: 91},
		{name: "one window low, one at its limit", five: 40, seven: 100},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.row(t, w.def, c.five, c.seven, w.clock.Add(24*time.Hour))
			w.land(t)
			want := 1 + c.lifts
			for range 4 {
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
			for range 4 {
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
	w.screen(t, w.def, wireDialog(t), "waiting")
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
	if len(first) != 1 || !strings.Contains(first[0].Question, "tries it again") {
		t.Fatalf("items %+v", first)
	}
	for *w.clock = stopped.Add(4*time.Hour + 40*time.Minute); w.clock.Before(stopped.Add(5*time.Hour - 6*time.Minute)); {
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
	for range 5 {
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
	w.screen(t, w.def, wireDialog(t), "waiting")
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
	w.screen(t, w.work, wireDialog(t), "waiting")
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
		w.screen(t, w.def, wireDialog(t), "waiting")
		w.land(t)
		w.pass(t, time.Minute)
		if it := w.items(t); len(it) != 1 || !strings.Contains(it[0].Question, "tries it again") {
			t.Fatalf("items %+v", it)
		}
		reset := w.clock.Add(8 * time.Hour).Truncate(time.Second)
		w.row(t, w.def, -1, 100, reset)
		w.pass(t, time.Minute)
		items := w.items(t)
		if len(items) != 1 || !strings.Contains(items[0].Question, reset.UTC().Format("Jan 2 15:04 UTC")) || strings.Contains(items[0].Question, "tries it again") {
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
		w.screen(t, w.def, wireDialog(t), "waiting")
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
	w.screen(t, w.def, wireDialog(t), "waiting")
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
	w.pass(t, selfContinueGrace+time.Minute)
	if got := len(w.starts(t)); got != 2 {
		t.Fatalf("%d landings after the stop could work", got)
	}
}

func TestWireASwitchToAutoSkipsALoginAnotherSessionBarred(t *testing.T) {
	w := newWireWorld(t)
	w.land(t)
	if err := recordStop(w.state, w.work, *w.clock, time.Time{}, nil); err != nil {
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
	if err := recordStop(w.state, w.def, *w.clock, time.Time{}, nil); err != nil {
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
	if err := recordStop(w.state, w.work, *w.clock, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	out, err := w.l.QueueProfileSwitch(w.rec.Key, "claude-work", "testing")
	if err != nil || !strings.Contains(out.Message, "out of its subscription limit") {
		t.Fatalf("switch %+v, %v", out, err)
	}
	if err = recordStop(w.state, w.def, *w.clock, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if err = recordStop(w.state, w.other, *w.clock, time.Time{}, nil); err != nil {
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
		{name: "a silent dialog whose turn has not ended", screen: wireDialog(t), turn: "user", wantRowPart: "has not ended"},
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
			if _, err := w.meter.Scan(time.Now()); err != nil {
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
	w.screen(t, w.def, wireDialog(t), "waiting")
	w.land(t)
	for _, p := range []profile.Profile{w.work, w.other} {
		putProfileFile(t, filepath.Join(p.Root, "hang"), "")
	}
	*w.clock = w.clock.Add(time.Minute)
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err = w.l.Failover(b); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Fatalf("Failover took %v on hung checks", took)
	}
	started = time.Now()
	if err = w.l.Failover(b); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took >= authBudget {
		t.Fatalf("a later pass waited %v on checks already past their budget", took)
	}
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
		t.Fatalf("session on %q", rec.Profile)
	}
	if row := w.rowText(t); !strings.Contains(row, "has not answered") {
		t.Fatalf("row %q", row)
	}
	items := w.items(t)
	if len(items) != 1 || !strings.HasPrefix(items[0].Rec, "wait: ") || !strings.Contains(items[0].Rec, "has not answered") || strings.Contains(items[0].Rec, "usage left") {
		t.Fatalf("the item says what it cannot know of a hung login: %+v", items)
	}
	for _, o := range items[0].Options {
		if strings.Contains(o.Effect, "ym switch") {
			t.Fatalf("the item offers what ym switch would refuse: %+v", o)
		}
	}
	for _, p := range []profile.Profile{w.work, w.other} {
		if err := os.Remove(filepath.Join(p.Root, "hang")); err != nil {
			t.Fatal(err)
		}
	}
	for deadline := time.Now().Add(15 * time.Second); ; time.Sleep(time.Second) {
		w.pass(t, time.Minute)
		if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("a login answering again did not take the session")
		}
	}
}

// walled is a real capture with its reset moved to six hours from now, so it holds whenever the test runs.
func (w *wireWorld) walled(t *testing.T, file string) (string, time.Time) {
	t.Helper()
	reset := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Hour)
	clock := strings.ToLower(reset.Format("3pm"))
	text := strings.ReplaceAll(wireScreen(t, file), "Nov 5, 3am", reset.Format("Jan 2, ")+clock)
	return strings.ReplaceAll(text, "3am", clock), reset
}

func (w *wireWorld) transcript(t *testing.T, p profile.Profile) string {
	t.Helper()
	return filepath.Join(p.Root, "projects", strings.NewReplacer("/", "-", ".", "-").Replace(w.cwd), profileConversation+".jsonl")
}

func wireWall(at time.Time) string {
	return fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}
{"type":"assistant","sessionId":"%s","timestamp":"%s","isApiErrorMessage":true,"error":"rate_limit","message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"You've hit your limit"}]}}
`, profileConversation, at.Add(-time.Second).UTC().Format(time.RFC3339Nano), profileConversation, at.UTC().Format(time.RFC3339Nano))
}

func (w *wireWorld) scan(t *testing.T) {
	t.Helper()
	if _, err := w.meter.Scan(time.Now()); err != nil {
		t.Fatal(err)
	}
}

// Each real stop on a walled login, whatever its row reads, is a stop: it bars the login with the reset the screen states, moves the session
// when another login can take it, and never restarts it in place while the bar stands.
func TestWireRealWalledScreensAreStopsAndNeverRestartInPlace(t *testing.T) {
	for _, screen := range []struct {
		name, file, status string
	}{
		{"A first landing, the footer", "walled-a-first-landing.txt", "idle"},
		{"C dialog after Esc", "walled-c-after-esc.txt", "waiting"},
		{"C dialog after an exit", "walled-c-after-exit.txt", "waiting"},
		{"the hit line right above the dialog", "walled-hit-line-dialog.txt", "waiting"},
	} {
		for _, used := range []float64{50, 100} {
			for _, elsewhere := range []bool{true, false} {
				t.Run(fmt.Sprintf("%s/row %.0f/elsewhere %v", screen.name, used, elsewhere), func(t *testing.T) {
					w := newWireWorld(t)
					putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute)))
					text, reset := w.walled(t, screen.file)
					w.row(t, w.def, -1, used, reset)
					if !elsewhere {
						w.exhaust(t, w.work, w.other)
					}
					w.screen(t, w.def, text, screen.status)
					w.land(t)
					w.scan(t)
					for range 4 {
						w.pass(t, time.Minute)
					}
					rec, _ := w.l.records.Find(w.rec.Key)
					landings := len(w.starts(t))
					if elsewhere && (rec.Profile == "default" || landings != 2) || !elsewhere && (rec.Profile != "default" || landings != 1) {
						t.Fatalf("on %q, landings %q", rec.Profile, w.starts(t))
					}
					bar := w.bar(t, w.def)
					if !bar.Until.Equal(reset) || !bar.Known || bar.Held {
						t.Fatalf("bar %+v, reset %v", bar, reset)
					}
					if !elsewhere && !strings.Contains(w.rowText(t), "no usable login, resets ") {
						t.Fatalf("row %q", w.rowText(t))
					}
				})
			}
		}
	}
}

// A dialog's stop cannot be proved healthy by the same row again and again: with rows refreshing, one wasted landing, then it holds.
func TestWireARefreshedRowCannotLoopARealDialog(t *testing.T) {
	for _, c := range []struct {
		name, file, status string
	}{
		{name: "same dialog, no new turn", file: "walled-hit-line-dialog.txt", status: "waiting"},
		{name: "silent dialog", file: "walled-c-after-exit.txt", status: "waiting"},
		{name: "first landing footer", file: "walled-a-first-landing.txt", status: "idle"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute)))
			text, reset := w.walled(t, c.file)
			w.screen(t, w.def, text, c.status)
			w.land(t)
			w.scan(t)
			w.row(t, w.def, -1, 50, reset)
			for range 8 {
				w.pass(t, time.Minute)
				w.row(t, w.def, -1, 50, reset)
			}
			if got := len(w.starts(t)); got != 1 {
				t.Fatalf("%d landings in 8 passes: %+v", got, w.bar(t, w.def))
			}
		})
	}
}

// Esc leaves an ordinary prompt with the stop in scrollback. That text neither makes nor extends a bar; a bar on record still shows.
func TestWireAPaneBackAtItsPromptReadsThroughTheRecord(t *testing.T) {
	for _, file := range []string{"walled-b-esc-footer.txt", "walled-d-esc-dialog.txt"} {
		for _, used := range []float64{50, 100} {
			t.Run(fmt.Sprintf("%s/row %.0f/stop on record", file, used), func(t *testing.T) {
				w := newWireWorld(t)
				w.exhaust(t, w.work, w.other)
				text, reset := w.walled(t, "walled-a-first-landing.txt")
				w.row(t, w.def, -1, used, reset)
				w.screen(t, w.def, text, "idle")
				w.land(t)
				w.pass(t, time.Minute)
				before := w.bar(t, w.def)
				after, _ := w.walled(t, file)
				w.screen(t, w.def, after, "idle")
				time.Sleep(300 * time.Millisecond)
				w.pass(t, time.Minute)
				w.pass(t, time.Minute)
				if row := w.rowText(t); !strings.Contains(row, "stopped at its limit earlier") || !strings.Contains(row, "resets ") {
					t.Fatalf("row %q", row)
				}
				if got := w.bar(t, w.def); !got.Stopped.Equal(before.Stopped) || !got.Until.Equal(before.Until) || len(w.starts(t)) != 1 {
					t.Fatalf("scrollback moved the bar: %+v then %+v, starts %q", before, got, w.starts(t))
				}
			})
			t.Run(fmt.Sprintf("%s/row %.0f/no stop on record", file, used), func(t *testing.T) {
				w := newWireWorld(t)
				text, reset := w.walled(t, file)
				w.row(t, w.def, -1, used, reset)
				w.screen(t, w.def, text, "idle")
				w.land(t)
				for range 3 {
					w.pass(t, time.Minute)
				}
				if row := w.rowText(t); row != "" {
					t.Fatalf("row %q", row)
				}
				if got := w.bar(t, w.def); !got.Stopped.IsZero() || len(w.starts(t)) != 1 {
					t.Fatalf("scrollback made a stop: %+v, starts %q", got, w.starts(t))
				}
			})
		}
	}
}

// Esc on a dialog with a prompt held sends it, which hits the wall and lands on the footer: a new stop in the same process.
func TestWireAHeldPromptAfterEscLandsOnTheFooter(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Hour)))
	dialog, _ := w.walled(t, "walled-c-after-esc.txt")
	w.screen(t, w.def, dialog, "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	first := w.bar(t, w.def)
	if first.Stopped.IsZero() {
		t.Fatal("no stop from the dialog")
	}
	footer, _ := w.walled(t, "walled-a-first-landing.txt")
	putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute))+wireWall(w.clock.Add(time.Second)))
	w.screen(t, w.def, footer, "idle")
	time.Sleep(300 * time.Millisecond)
	w.pass(t, time.Minute)
	w.pass(t, time.Minute)
	if got := w.bar(t, w.def); !got.Stopped.After(first.Stopped) || !got.Held {
		t.Fatalf("the footer was no new stop: %+v after %+v", got, first)
	}
	if got := len(w.starts(t)); got != 1 {
		t.Fatalf("%d landings", got)
	}
}

// How long a healthy login stays barred when rows read 10% every minute from the first stop on.
func TestWireHowLongAHealthyLoginStaysBarred(t *testing.T) {
	week := 7 * 24 * time.Hour
	type step struct {
		after time.Duration
		stop  bool
		reset time.Duration
		row   float64
	}
	for _, c := range []struct {
		name   string
		steps  []step
		barred time.Duration
		// lifted is a bar the next row ends at once, after the time above.
		lifted bool
	}{
		{name: "U1 one false stop", steps: []step{{stop: true}}, lifted: true},
		{name: "U2 two false stops a minute apart", steps: []step{{stop: true}, {after: time.Minute, stop: true}}, barred: 5*time.Hour + time.Minute},
		{name: "U3 two false stops 4h59 apart", steps: []step{{stop: true}, {after: 4*time.Hour + 59*time.Minute, stop: true}}, barred: 9*time.Hour + 59*time.Minute},
		{name: "U4 two false stops 5h01 apart", steps: []step{{stop: true}, {after: 5*time.Hour + time.Minute, stop: true}}, barred: 10*time.Hour + time.Minute},
		{name: "U5 two false stops 11h apart", steps: []step{{stop: true}, {after: 11 * time.Hour, stop: true}}, barred: 11 * time.Hour, lifted: true},
		{name: "K1 two stops, a weekly row, then 10%", steps: []step{{stop: true}, {after: time.Minute, stop: true}, {after: time.Minute, row: 100}}, barred: 5*time.Hour + time.Minute},
		{name: "K2 a weekly row disproved, then one false stop", steps: []step{{row: 100}, {after: time.Minute, row: 10}, {after: time.Minute, stop: true}}, barred: 2 * time.Minute, lifted: true},
		{name: "K3 an unknown stop, then a stop with a weekly reset", steps: []step{{stop: true}, {after: time.Minute, stop: true, reset: week - time.Hour}}, barred: 5*time.Hour + time.Minute},
		{name: "K4 an unknown stop, then a reset a year ahead", steps: []step{{stop: true}, {after: time.Minute, stop: true, reset: 365 * 24 * time.Hour}}, barred: 5*time.Hour + time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			start := *w.clock
			for _, s := range c.steps {
				*w.clock = w.clock.Add(s.after)
				if s.row != 0 {
					w.row(t, w.def, -1, s.row, start.Add(week))
				}
				if s.stop {
					var reset time.Time
					if s.reset != 0 {
						reset = w.clock.Add(s.reset)
					}
					if err := recordStop(w.state, w.def, *w.clock, reset, w.l.limits()); err != nil {
						t.Fatal(err)
					}
				}
				w.pass(t, 0)
			}
			usableAt := func(at time.Time) bool {
				*w.clock = at
				w.row(t, w.def, -1, 10, start.Add(week))
				*w.clock = at.Add(2 * time.Second)
				return barredUntil(w.state, w.def, w.l.limits(), *w.clock).IsZero()
			}
			end := start.Add(c.barred)
			if !c.lifted && usableAt(end.Add(-time.Minute)) {
				t.Fatalf("usable a minute before %v: %+v", c.barred, w.bar(t, w.def))
			}
			if !usableAt(end.Add(time.Minute)) {
				t.Fatalf("still barred a minute past %v, to %v: %+v", c.barred, barredUntil(w.state, w.def, w.l.limits(), *w.clock).Sub(start), w.bar(t, w.def))
			}
		})
	}
}

// A false stop is no reason to rewrite a conversation under a new id: a session that merely exited waits for it, unless the bar has evidence.
func TestWireExitedSessionsPortOnlyOnABarWithEvidence(t *testing.T) {
	for _, c := range []struct {
		name          string
		records, want int
		evidence      func(*testing.T, *wireWorld)
	}{
		{name: "the real host's shape, one false stop", records: 73, want: 0, evidence: func(t *testing.T, w *wireWorld) {
			if err := recordStop(w.state, w.work, *w.clock, time.Time{}, nil); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a row at its limit", records: 2, want: 2, evidence: func(t *testing.T, w *wireWorld) { w.exhaust(t, w.work) }},
		{name: "a reset the login stated", records: 2, want: 2, evidence: func(t *testing.T, w *wireWorld) {
			if err := recordStop(w.state, w.work, *w.clock, w.clock.Add(time.Hour), nil); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.addCodex(t)
			w.exhaust(t, w.def, w.other)
			c.evidence(t, w)
			rel := strings.NewReplacer("/", "-", ".", "-").Replace(w.cwd)
			for i := range c.records {
				id := fmt.Sprintf("33333333-3333-4333-8333-%012d", i)
				rec := launch.Record{Key: fmt.Sprintf("r%d", i), Harness: "claude", Session: id, Running: true, Worktree: launch.Worktree{Path: w.cwd}}
				setRecordProfile(&rec, w.work)
				if err := w.l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				putProfileFile(t, filepath.Join(w.work.Root, "projects", rel, id+".jsonl"), wireTurn(id, w.clock.Add(-time.Hour)))
			}
			if err := w.l.records.Delete(w.rec.Key); err != nil {
				t.Fatal(err)
			}
			ported := map[string]bool{}
			w.c.PreparePort = func(s core.Session, to, id string) (core.PortPlan, error) {
				ported[s.ID] = true
				return core.PortPlan{}, errors.New("counted, not written")
			}
			*w.clock = w.clock.Add(11 * time.Second)
			b, err := w.c.Board()
			if err != nil {
				t.Fatal(err)
			}
			if err = w.c.ReviveWork(b); err != nil {
				t.Log(err)
			}
			if len(ported) != c.want {
				t.Fatalf("%d of %d records would port, want %d", len(ported), c.records, c.want)
			}
		})
	}
}

func TestWireAFreshLaunchIsNotRefusedOverGuesses(t *testing.T) {
	w := newWireWorld(t)
	for _, p := range []profile.Profile{w.def, w.work, w.other} {
		if err := recordStop(w.state, p, *w.clock, time.Time{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := w.l.selectProfile("claude", ""); err != nil || p.Harness != "claude" {
		t.Fatalf("every login guessed out: %+v, %v", p, err)
	}
	w.exhaust(t, w.def, w.work, w.other)
	if _, err := w.l.selectProfile("claude", ""); err == nil {
		t.Fatal("every login with a row at its limit, yet a login was chosen")
	}
}

func TestWireAnOwnerItemRespectsItsDismissalWithinItsOwnStop(t *testing.T) {
	for _, how := range []string{core.ItemDismissed, core.ItemAnswered} {
		t.Run(how, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.land(t)
			w.pass(t, time.Minute)
			first := w.items(t)
			if len(first) != 1 {
				t.Fatalf("items %+v", first)
			}
			if _, err := w.c.Store.Transition(first[0].Key(), how, "not now"); err != nil {
				t.Fatal(err)
			}
			question := first[0].Question
			for range 3 {
				w.pass(t, time.Minute)
			}
			w.row(t, w.def, -1, 100, w.clock.Add(48*time.Hour))
			w.pass(t, time.Minute)
			if items := w.items(t); len(items) != 1 || items[0].Question != question {
				t.Fatalf("within its stop: %+v", items)
			}
			if err := recordStop(w.state, w.def, *w.clock, time.Time{}, nil); err != nil {
				t.Fatal(err)
			}
			w.pass(t, time.Minute)
			open := 0
			for _, it := range w.items(t) {
				if it.State == core.ItemOpen {
					open++
				}
			}
			if open != 1 {
				t.Fatalf("%d open on a live wall: %+v", open, w.items(t))
			}
		})
	}
}

func TestWireAReloginBarsTheAccountThatStopped(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	w.land(t)
	old := w.def
	putProfileFile(t, filepath.Join(w.def.Root, ".claude.json"), `{"oauthAccount":{"accountUuid":"fixture-user","organizationUuid":"relogged"}}`)
	fresh := old
	fresh.Account = profile.Identity(old)
	if fresh.Account == old.Account {
		t.Fatal("the fixture did not change the account")
	}
	if err := updateBar(w.state, old, func(b *loginBar) error { b.Item = "stale"; return nil }); err != nil {
		t.Fatal(err)
	}
	w.screen(t, w.def, wireDialog(t), "waiting")
	time.Sleep(300 * time.Millisecond)
	for range 2 {
		w.pass(t, time.Minute)
	}
	if got := w.bar(t, fresh); !got.Stopped.IsZero() {
		t.Fatalf("the new account got the old pane's bar: %+v", got)
	}
	if got := w.bar(t, old); got.Stopped.IsZero() || got.Item != "" {
		t.Fatalf("the old account: %+v", got)
	}
}

func TestWireOneStopGoingFromUnprovableToProvableIsOneStop(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	path := w.transcript(t, w.def)
	putProfileFile(t, path, fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}`+"\n", profileConversation, w.clock.UTC().Format(time.RFC3339Nano)))
	w.screen(t, w.def, wireDialog(t), "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	first := w.bar(t, w.def)
	if first.Stopped.IsZero() {
		t.Fatal("no stop recorded")
	}
	putProfileFile(t, path, wireTurn(profileConversation, w.clock.Add(time.Second)))
	w.pass(t, time.Minute)
	if got := w.bar(t, w.def); !got.Stopped.Equal(first.Stopped) || got.Held {
		t.Fatalf("one stop counted twice: %+v after %+v", got, first)
	}
}

func TestWireTheDialogsOwnResetIsRead(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	text, reset := w.walled(t, "walled-c-after-exit.txt")
	w.screen(t, w.def, text, "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	if bar := w.bar(t, w.def); !bar.Known || !bar.Until.Equal(reset) {
		t.Fatalf("bar %+v, reset %v", bar, reset)
	}
	if items := w.items(t); len(items) != 1 || !strings.Contains(items[0].Question, "resets "+reset.Format("Jan 2 15:04 UTC")) {
		t.Fatalf("items %+v", items)
	}
}

func TestWireItemsStayTrueWhenAnotherLoginChanges(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.other)
	w.screen(t, w.def, wireDialog(t), "waiting")
	w.screen(t, w.work, wireDialog(t), "waiting")
	w.land(t)
	for range 3 {
		w.pass(t, time.Minute)
	}
	items := w.items(t)
	if len(items) != 2 {
		t.Fatalf("items %+v", items)
	}
	for _, it := range items {
		if strings.Contains(it.Rec, "usage left") {
			t.Fatalf("every login barred, yet %q", it.Rec)
		}
		for _, o := range it.Options {
			if strings.Contains(o.Label, "not yet known") {
				t.Fatalf("option %q", o.Label)
			}
		}
	}
	w.exhaust(t, w.def)
	w.pass(t, time.Minute)
	it, err := w.c.Store.Item(items[0].Key())
	if err != nil {
		t.Fatal(err)
	}
	told := 0
	for _, e := range it.History {
		if strings.Contains(e.Text, "now resets") {
			told++
		}
	}
	if !strings.Contains(it.Question, "and resets ") || told != 1 {
		t.Fatalf("question %q, told %d times: %+v", it.Question, told, it.History)
	}
}

func TestAResetBeyondTheLongestWindowIsNotBelieved(t *testing.T) {
	now := time.Date(2026, 11, 4, 3, 1, 0, 0, time.UTC)
	for _, c := range []struct {
		name  string
		reset time.Duration
		want  time.Duration
		known bool
	}{
		{name: "a week ahead", reset: longestWindow, want: longestWindow, known: true},
		{name: "a year ahead", reset: 365 * 24 * time.Hour, want: unknownResetBar},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			p := profile.Profile{Harness: "claude", ID: "default", Account: "claude/u/a"}
			if err := recordStop(dir, p, now, now.Add(c.reset), nil); err != nil {
				t.Fatal(err)
			}
			b, err := readBar(barPath(dir, p))
			if err != nil || !b.Until.Equal(now.Add(c.want)) || b.Known != c.known {
				t.Fatalf("%+v, %v", b, err)
			}
		})
	}
}

func TestOneLandingClaimsTheAttemptAfterAStop(t *testing.T) {
	dir := t.TempDir()
	p := profile.Profile{Harness: "claude", ID: "default", Account: "claude/u/a"}
	now := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	if err := recordStop(dir, p, now, now.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	past := now.Add(time.Hour + time.Second)
	if !barredUntil(dir, p, nil, past).IsZero() {
		t.Fatal("still barred past its reset")
	}
	if err := claimLogin(dir, p, past); err != nil {
		t.Fatal(err)
	}
	if barredUntil(dir, p, nil, past.Add(time.Minute)).IsZero() {
		t.Fatal("a second landing could go while the first is being tested")
	}
	over := past.Add(selfContinueGrace + time.Second)
	if !barredUntil(dir, p, nil, over).IsZero() {
		t.Fatal("no stop came back, yet the login stays held")
	}
	if err := claimLogin(dir, p, over); err != nil {
		t.Fatal(err)
	}
	if !barredUntil(dir, p, nil, over.Add(time.Minute)).IsZero() {
		t.Fatal("a stop's attempt was claimed twice")
	}
}

// Claude stores its credentials where this variable points, so an inherited one would run a login on another's account.
func TestWireALaunchNeverKeepsAnInheritedCredentialStore(t *testing.T) {
	for _, c := range []struct {
		name  string
		login string
		fresh bool
	}{
		{name: "default, inherited after its state dir", login: "default"},
		{name: "claude-work, inherited after its state dir", login: "claude-work"},
		{name: "default, inherited as its state dir was made", login: "default", fresh: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w *wireWorld
			if c.fresh {
				w = newWireWorld(t, "/wrong/credentials")
			} else {
				w = newWireWorld(t)
				t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "/wrong/credentials")
			}
			if c.login != "default" {
				setRecordProfile(&w.rec, w.work)
				if err := w.l.records.Save(w.rec); err != nil {
					t.Fatal(err)
				}
				putProfileFile(t, w.transcript(t, w.work), wireTurn(profileConversation, w.clock.Add(-time.Minute)))
			}
			w.land(t)
			raw, err := os.ReadFile(w.log + ".env")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "/wrong/credentials") {
				t.Fatalf("launched with the inherited store: %s", raw)
			}
		})
	}
}

// countingHost counts the panes captured, each a few milliseconds on every tick.
type countingHost struct {
	core.Host
	screens *int
}

func (h countingHost) Screen(handle core.Handle) (string, error) {
	*h.screens++
	return h.Host.Screen(handle)
}

func TestAnIdleSessionIsCapturedOnceAMinuteAndAtOnceWhenItGoesIdleAgain(t *testing.T) {
	w := newWireWorld(t)
	w.land(t)
	screens := 0
	for i, h := range w.c.Hosts {
		w.c.Hosts[i] = countingHost{h, &screens}
	}
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := b.Session(profileConversation)
	since := s.Since
	look := func(at time.Time) {
		for i := range b.Sessions {
			if b.Sessions[i].ID == profileConversation {
				b.Sessions[i].Since = since
			}
		}
		b.At = at
		if _, _, unlooked := w.l.silentStops(b); len(unlooked) != 0 {
			t.Fatalf("a pane whose last look found no stop held its login: %v", unlooked)
		}
	}
	base := b.At
	for _, c := range []struct {
		name string
		at   time.Duration
		idle time.Duration
		want int
	}{
		{name: "the first look captures", want: 1},
		{name: "ten seconds on does not", at: 10 * time.Second},
		{name: "a minute on does", at: time.Minute, want: 1},
		{name: "a new turn's end does at once", at: time.Minute + time.Second, idle: time.Hour, want: 1},
	} {
		screens = 0
		since = s.Since.Add(c.idle)
		look(base.Add(c.at))
		if screens != c.want {
			t.Fatalf("%s: %d captures, want %d", c.name, screens, c.want)
		}
	}
}

// Esc cancels Claude's wait, not failover: with a real stop in its transcript and a row at its limit the pane moves if a login can take it.
func TestWireEscOnAStopDoesNotOptOutOfFailover(t *testing.T) {
	for _, file := range []string{"walled-b-esc-footer.txt", "walled-d-esc-dialog.txt"} {
		for _, c := range []struct {
			name      string
			used      float64
			elsewhere bool
			on        string
			stopped   bool
		}{
			{name: "row 100, a login usable", used: 100, elsewhere: true, on: "claude-work", stopped: true},
			{name: "row 100, nowhere to go", used: 100, on: "default", stopped: true},
			{name: "row 50, a login usable", used: 50, elsewhere: true, on: "default"},
			{name: "row 50, nowhere to go", used: 50, on: "default"},
		} {
			t.Run(file+"/"+c.name, func(t *testing.T) {
				w := newWireWorld(t)
				putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute)))
				text, reset := w.walled(t, file)
				w.row(t, w.def, -1, c.used, reset)
				if !c.elsewhere {
					w.exhaust(t, w.work, w.other)
				}
				w.screen(t, w.def, text, "idle")
				w.land(t)
				w.scan(t)
				for range 3 {
					w.pass(t, time.Minute)
				}
				rec, _ := w.l.records.Find(w.rec.Key)
				if rec.Profile != c.on || w.bar(t, w.def).Stopped.IsZero() == c.stopped {
					t.Fatalf("on %q, bar %+v, landings %q", rec.Profile, w.bar(t, w.def), w.starts(t))
				}
			})
		}
	}
}

// addCodex configures a usable login of the other harness, the last resort.
func (w *wireWorld) addCodex(t *testing.T) {
	t.Helper()
	cx := profile.Profile{Harness: "codex", ID: "codex-main", Root: t.TempDir(), Explicit: true}
	if err := profile.Add(w.state, cx); err != nil {
		t.Fatal(err)
	}
	cx.Root = physicalProfileRoot(cx.Root)
	subscriptionIdentity(t, &cx)
}

// land is one of the routes a session takes off a login whose check is being asked.
type portRoute struct {
	name string
	// ready leaves the session on the wall, ready for attempts.
	ready func(*testing.T, *wireWorld)
	// attempt is one try to move it: a pass, or a revive.
	attempt func(*testing.T, *wireWorld) error
}

func portRoutes() []portRoute {
	pass := func(t *testing.T, w *wireWorld) error {
		w.pass(t, 20*time.Second)
		return nil
	}
	return []portRoute{
		{name: "a stop on screen", attempt: pass, ready: func(t *testing.T, w *wireWorld) {
			w.exhaust(t, w.other)
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.land(t)
		}},
		{name: "revive of an exited session", ready: func(t *testing.T, w *wireWorld) {
			w.exhaust(t, w.def, w.other)
		}, attempt: func(t *testing.T, w *wireWorld) error {
			*w.clock = w.clock.Add(20 * time.Second)
			rec, _ := w.l.records.Find(w.rec.Key)
			if rec.Profile != "default" {
				return nil
			}
			_, err := w.l.revive(rec)
			return err
		}},
		{name: "a queued switch whose login ran out", attempt: pass, ready: func(t *testing.T, w *wireWorld) {
			w.land(t)
			rec, _ := w.l.records.Find(w.rec.Key)
			if err := saveMigration(w.state, migration{Version: 1, Source: rec, Destination: w.other, State: "queued", Since: *w.clock}); err != nil {
				t.Fatal(err)
			}
			if err := saveFailover(w.state, "claude", failoverAttempt{Event: "e", State: core.FailoverState{Session: profileConversation, Profile: "default", Account: w.def.Account, Status: "queued", Target: w.other.ID}}); err != nil {
				t.Fatal(err)
			}
			w.exhaust(t, w.def, w.other)
		}},
	}
}

// countPorts makes ports countable, with no fake cs to write them.
func countPorts(w *wireWorld) *int {
	ports := 0
	w.c.PreparePort = func(core.Session, string, string) (core.PortPlan, error) {
		ports++
		return core.PortPlan{}, errors.New("counted, not written")
	}
	return &ports
}

// The one gate on a port, by every route: only positive evidence about every other login of the harness opens it, and anything else keeps the
// session on its login with the reason on its row.
func TestWirePortGate(t *testing.T) {
	put := func(file string) func(*testing.T, *wireWorld) {
		return func(t *testing.T, w *wireWorld) { putProfileFile(t, filepath.Join(w.work.Root, file), "") }
	}
	for _, c := range []struct {
		name    string
		setup   func(*testing.T, *wireWorld)
		timeout time.Duration
		lands   bool
		ports   bool
		// everyRoute runs it by every route.
		everyRoute bool
	}{
		{name: "evidence: a row at its limit", ports: true, everyRoute: true, setup: func(t *testing.T, w *wireWorld) { w.exhaust(t, w.work) }},
		{name: "evidence: a stated reset", ports: true, setup: func(t *testing.T, w *wireWorld) {
			if err := recordStop(w.state, w.work, *w.clock, w.clock.Add(time.Hour), nil); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "evidence: an answered sign-out", ports: true, setup: put("out")},
		{name: "no row at all is no evidence", lands: true, setup: func(*testing.T, *wireWorld) {}},
		{name: "unknown: times out once, then answers usable", lands: true, everyRoute: true, setup: put("slow")},
		{name: "unknown: always takes 5s", lands: true, setup: put("five")},
		{name: "unknown: never answers", everyRoute: true, setup: put("hang")},
		{name: "unknown: slower than the timeout", timeout: 2 * time.Second, setup: put("hang")},
		{name: "unknown: a status that exits non-zero", setup: put("fail")},
		{name: "unknown: a status check that was killed", setup: put("killed")},
		{name: "unknown: another session's guessed bar", setup: func(t *testing.T, w *wireWorld) {
			if err := recordStop(w.state, w.work, *w.clock, time.Time{}, nil); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown: a claim", setup: func(t *testing.T, w *wireWorld) {
			if err := updateBar(w.state, w.work, func(b *loginBar) error { b.Claim = w.clock.Add(time.Hour); return nil }); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unknown: a truncated identity", setup: func(t *testing.T, w *wireWorld) {
			putProfileFile(t, filepath.Join(w.work.Root, ".claude.json"), "{")
		}},
		{name: "unknown: a truncated settings file", setup: func(t *testing.T, w *wireWorld) {
			putProfileFile(t, filepath.Join(w.work.Root, "settings.json"), "{")
		}},
	} {
		for i, route := range portRoutes() {
			// The gate is one function: every kind by one route, and each outcome by every route.
			if i > 0 && !c.everyRoute {
				continue
			}
			t.Run(route.name+"/"+c.name, func(t *testing.T) {
				w := newWireWorld(t)
				w.addCodex(t)
				w.l.statuses.timeout = c.timeout
				ports := countPorts(w)
				route.ready(t, w)
				c.setup(t, w)
				landed := func() bool {
					rec, _ := w.l.records.Find(w.rec.Key)
					return rec.Profile == "claude-work"
				}
				var last error
				for i, deadline := 0, time.Now().Add(9*time.Second); (i < 3 || c.lands && time.Now().Before(deadline)) && !landed() && *ports == 0; i++ {
					last = route.attempt(t, w)
					if c.lands {
						time.Sleep(time.Second)
					}
				}
				if landed() != c.lands || (*ports > 0) != c.ports {
					t.Fatalf("on claude-work %v, %d ports; want %v, %v; last %v, row %q", landed(), *ports, c.lands, c.ports, last, w.rowText(t))
				}
				if !c.lands && !c.ports && route.name == "a stop on screen" && !strings.Contains(w.rowText(t), "stopped at its limit: ") {
					t.Fatalf("no reason on the row: %q", w.rowText(t))
				}
				if strings.Contains(c.name, "killed") && route.name == "a stop on screen" && !strings.Contains(w.rowText(t), "has not answered") {
					t.Fatalf("a killed check reads as an answer: %q", w.rowText(t))
				}
			})
		}
	}
}

// A switch that fails is no answer from the login. It is tried again, and never ports while it can be.
func TestWireAFailedSwitchNeverPortsWhileItsLoginCanBeTriedAgain(t *testing.T) {
	for _, route := range portRoutes() {
		t.Run(route.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.addCodex(t)
			ports := countPorts(w)
			route.ready(t, w)
			fails := 1
			if route.name == "revive of an exited session" {
				list := w.c.Procs
				w.c.Procs = func() ([]proc.Proc, error) {
					if fails > 0 {
						fails--
						return nil, errors.New("the process table could not be read")
					}
					return list()
				}
			} else {
				w.c.Signal = func(pid int, sig syscall.Signal) error {
					if fails > 0 {
						fails--
						return errors.New("the signal did not go")
					}
					return syscall.Kill(pid, sig)
				}
			}
			var seen []string
			for range 6 {
				seen = append(seen, fmt.Sprint(route.attempt(t, w)))
				if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile == "claude-work" {
					break
				}
			}
			if rec, _ := w.l.records.Find(w.rec.Key); *ports != 0 || rec.Profile != "claude-work" || fails != 0 {
				t.Fatalf("%d ports, on %q, fails left %d, row %q: %q", *ports, rec.Profile, fails, w.rowText(t), seen)
			}
		})
	}
}

// A failover's queued port goes through the gate again until it applies.
func TestWireAQueuedPortIsReDecidedWhenALoginOfItsHarnessComesBack(t *testing.T) {
	w := newWireWorld(t)
	w.addCodex(t)
	w.land(t)
	ports := countPorts(w)
	ps, err := profile.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	cx, err := profile.Find(ps, "codex", "codex-main")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := w.l.records.Find(w.rec.Key)
	if err = saveMigration(w.state, migration{Version: 1, Source: rec, Destination: cx, State: "queued", Since: *w.clock}); err != nil {
		t.Fatal(err)
	}
	if err = saveFailover(w.state, "claude", failoverAttempt{Event: "e", On: w.def.Account, State: core.FailoverState{Session: profileConversation, Profile: "default", Account: w.def.Account, Status: "queued", Target: cx.ID}}); err != nil {
		t.Fatal(err)
	}
	w.exhaust(t, w.def, w.other)
	if err = recordStop(w.state, w.work, *w.clock, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	w.pass(t, time.Minute)
	if m, _ := loadMigration(w.state, "claude", profileConversation); !strings.Contains(m.Blocked, "may still have usage") || *ports != 0 {
		t.Fatalf("migration %+v, %d ports", m, *ports)
	}
	w.row(t, w.work, -1, 10, w.clock.Add(24*time.Hour))
	w.pass(t, time.Minute)
	w.pass(t, time.Minute)
	if rec, _ = w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" || *ports != 0 {
		t.Fatalf("on %q, %d ports", rec.Profile, *ports)
	}
}

// addSession lands another conversation on the login, at the screen its login prints.
func (w *wireWorld) addSession(t *testing.T, i int, p profile.Profile) string {
	t.Helper()
	id := fmt.Sprintf("44444444-4444-4444-8444-%012d", i)
	rec := launch.Record{Key: fmt.Sprintf("more%d", i), Harness: "claude", Session: id, Worktree: launch.Worktree{Path: w.cwd}}
	setRecordProfile(&rec, p)
	if err := w.l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	putProfileFile(t, filepath.Join(p.Root, "projects", strings.NewReplacer("/", "-", ".", "-").Replace(w.cwd), id+".jsonl"), wireTurn(id, w.clock.Add(-time.Minute)))
	once := w.l
	once.anyway = true
	if _, err := once.revive(rec); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestWireOneFailedLandingStopsNobody(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	w.screen(t, w.def, wireDialog(t), "waiting")
	w.land(t)
	more := []string{w.addSession(t, 1, w.def), w.addSession(t, 2, w.def)}
	time.Sleep(time.Second)
	w.pass(t, time.Minute)
	*w.clock = w.bar(t, w.def).Until
	path := os.Getenv("PATH")
	t.Setenv("PATH", t.TempDir()+":/usr/bin:/bin")
	for range 3 {
		*w.clock = w.clock.Add(time.Minute)
		b, err := w.c.Board()
		if err != nil {
			t.Fatal(err)
		}
		if err = w.l.Failover(b); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", path)
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range append(more, profileConversation) {
		if _, alive := b.Session(id); !alive {
			t.Fatalf("session %s was stopped for a landing that could not happen", id)
		}
	}
	if bar := w.bar(t, w.def); !bar.Claim.After(*w.clock) {
		t.Fatalf("the failed landing gave up the claim: %+v", bar)
	}
}

func TestWireTheFirstLandingAfterAStopClaimsIt(t *testing.T) {
	t.Run("continuing in place", func(t *testing.T) {
		w := newWireWorld(t)
		w.exhaust(t, w.work, w.other)
		w.screen(t, w.def, wireDialog(t), "waiting")
		w.land(t)
		w.addSession(t, 1, w.def)
		time.Sleep(time.Second)
		w.pass(t, time.Minute)
		*w.clock = w.bar(t, w.def).Until
		before := len(w.starts(t))
		for range 3 {
			w.pass(t, 20*time.Second)
		}
		if got := len(w.starts(t)) - before; got != 1 {
			t.Fatalf("%d landings inside the claim", got)
		}
	})
	t.Run("moving onto a login whose stop was never tested", func(t *testing.T) {
		w := newWireWorld(t)
		w.addCodex(t)
		ports := countPorts(w)
		w.exhaust(t, w.other)
		if err := recordStop(w.state, w.work, w.clock.Add(-50*time.Hour), time.Time{}, nil); err != nil {
			t.Fatal(err)
		}
		w.screen(t, w.def, wireDialog(t), "waiting")
		w.land(t)
		second := w.addSession(t, 1, w.def)
		time.Sleep(time.Second)
		for range 3 {
			w.pass(t, 20*time.Second)
		}
		rec, _ := w.l.records.Find(second)
		first, _ := w.l.records.Find(w.rec.Key)
		if *ports != 0 || first.Profile == "claude-work" && rec.Profile == "claude-work" {
			t.Fatalf("%d ports; on %q and %q", *ports, first.Profile, rec.Profile)
		}
	})
}

// A live stop is a wall now, so a reset that reads as already passed is not known: the real footer's bare clock, seen in the hour after that clock.
func TestWireALiveWallWhoseClockJustPassedKeepsItsBar(t *testing.T) {
	for _, screen := range []struct{ file, status string }{{"walled-a-first-landing.txt", "idle"}, {"walled-c-after-exit.txt", "waiting"}} {
		for _, used := range []float64{-1, 50, 99} {
			t.Run(fmt.Sprintf("%s/row %v", screen.file, used), func(t *testing.T) {
				w := newWireWorld(t)
				w.exhaust(t, w.work, w.other)
				clock := strings.ToLower(time.Now().UTC().Add(-30 * time.Minute).Format("3:04pm"))
				w.screen(t, w.def, strings.ReplaceAll(wireScreen(t, screen.file), "3am", clock), screen.status)
				if used >= 0 {
					w.row(t, w.def, -1, used, w.clock.Add(24*time.Hour))
				}
				w.land(t)
				for range 3 {
					w.pass(t, time.Minute)
				}
				bar := w.bar(t, w.def)
				if got := len(w.starts(t)); got != 1 || bar.Known || !bar.Until.After(*w.clock) {
					t.Fatalf("%d landings, bar %+v", got, bar)
				}
			})
		}
	}
}

// The dialog's second option has varied, so what identifies it is the rest; with no time in it the reset comes from the hit-limit line.
func TestWireTheDialogAfterAResumeWithANoteIsAStop(t *testing.T) {
	for _, used := range []float64{50, 100} {
		for _, elsewhere := range []bool{true, false} {
			t.Run(fmt.Sprintf("row %.0f/elsewhere %v", used, elsewhere), func(t *testing.T) {
				w := newWireWorld(t)
				putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute)))
				text, reset := w.walled(t, "walled-dont-continue.txt")
				w.row(t, w.def, -1, used, reset)
				if !elsewhere {
					w.exhaust(t, w.work, w.other)
				}
				w.screen(t, w.def, text, "waiting")
				w.land(t)
				w.scan(t)
				for range 3 {
					w.pass(t, time.Minute)
				}
				rec, _ := w.l.records.Find(w.rec.Key)
				if elsewhere != (rec.Profile == "claude-work") {
					t.Fatalf("on %q, row %q", rec.Profile, w.rowText(t))
				}
				if bar := w.bar(t, w.def); !bar.Known || !bar.Until.Equal(reset) {
					t.Fatalf("bar %+v, reset %v", bar, reset)
				}
			})
		}
	}
}

// A row can lift a bar only while no stop shows on a screen, and a stop's stated reset stands against it until then. A pass that did not look
// at a pane says nothing: only a look that found the stop gone does.
func TestWireARowCannotLiftAStopShowingOnScreen(t *testing.T) {
	for _, c := range []struct {
		name string
		step time.Duration
		// quiet is an idle session on the login that a pass skips between looks.
		quiet bool
		// unread makes the pane unreadable on the passes that look.
		unread bool
	}{
		{name: "looked at every pass", step: time.Minute},
		{name: "passes closer than an idle pane is captured", step: 15 * time.Second},
		{name: "another idle session the passes skip", step: 15 * time.Second, quiet: true},
		{name: "a screen that cannot be read", step: 15 * time.Second, unread: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			text, reset := w.walled(t, "walled-a-first-landing.txt")
			w.screen(t, w.def, text, "idle")
			w.land(t)
			if c.quiet {
				w.screen(t, w.def, wireIdle, "idle")
				w.addSession(t, 1, w.def)
				time.Sleep(500 * time.Millisecond)
				w.screen(t, w.def, text, "idle")
				time.Sleep(300 * time.Millisecond)
			}
			w.pass(t, c.step)
			hosts := w.c.Hosts
			if c.unread {
				w.c.Hosts = nil
			}
			for range 3 {
				w.row(t, w.def, -1, 50, reset)
				w.pass(t, c.step)
				if bar := w.bar(t, w.def); !bar.Known || !bar.Until.Equal(reset) || barredUntil(w.state, w.def, w.l.limits(), *w.clock).IsZero() {
					t.Fatalf("a row lifted a stop on screen: %+v", bar)
				}
			}
			w.c.Hosts = hosts
			w.pass(t, c.step)
			items := w.items(t)
			if len(items) != 1 || items[0].State != core.ItemOpen || !strings.Contains(items[0].Question, "resets ") {
				t.Fatalf("items %+v", items)
			}
			if row := w.rowText(t); !strings.Contains(row, "no usable login, resets "+quotaReset(reset)) {
				t.Fatalf("row %q", row)
			}
			after, _ := w.walled(t, "walled-b-esc-footer.txt")
			w.screen(t, w.def, after, "idle")
			time.Sleep(400 * time.Millisecond)
			for range 3 {
				w.row(t, w.def, -1, 50, reset)
				w.pass(t, c.step)
			}
			if got := barredUntil(w.state, w.def, w.l.limits(), *w.clock); !got.IsZero() {
				t.Fatalf("still barred with the stop gone and a newer row: %+v", w.bar(t, w.def))
			}
			if it, _ := w.c.Store.Item(items[0].Key()); it.State == core.ItemOpen {
				t.Fatalf("the item outlived the bar: %+v", it)
			}
		})
	}
}

// Esc cancels Claude's wait, and a stop ym saw stays a stop though only the record holds it now.
func TestWireASilentDialogSeenBeforeEscStaysAStop(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.def, w.work, w.other)
	text, _ := w.walled(t, "walled-c-after-esc.txt")
	w.screen(t, w.def, text, "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	after, _ := w.walled(t, "walled-d-esc-dialog.txt")
	w.screen(t, w.def, after, "idle")
	time.Sleep(300 * time.Millisecond)
	w.pass(t, time.Minute)
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
		t.Fatalf("on %q", rec.Profile)
	}
	w.row(t, w.work, -1, 10, w.clock.Add(24*time.Hour))
	for range 2 {
		w.pass(t, time.Minute)
	}
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" {
		t.Fatalf("still on %q, row %q", rec.Profile, w.rowText(t))
	}
}

// A switch copies the transcript, so a stop line older than the session's arrival is the other login's history.
func TestWireACopiedStopLineIsChargedToNoOne(t *testing.T) {
	for _, c := range []struct {
		name  string
		fresh bool
	}{{name: "a copied line"}, {name: "a stop after it arrived", fresh: true}} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			putProfileFile(t, w.transcript(t, w.def), wireWall(time.Now().Add(-time.Hour)))
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.land(t)
			w.pass(t, time.Minute)
			if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" {
				t.Fatalf("on %q", rec.Profile)
			}
			line := strings.ReplaceAll(wireWall(time.Now().Add(-time.Hour)), `"isApiErrorMessage"`, `"copy":1,"isApiErrorMessage"`)
			if c.fresh {
				line = wireWall(time.Now().Add(time.Minute))
			}
			f, err := os.OpenFile(w.transcript(t, w.work), os.O_APPEND|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			fmt.Fprint(f, line)
			f.Close()
			w.scan(t)
			w.row(t, w.work, -1, 100, w.clock.Add(24*time.Hour))
			w.pass(t, time.Minute)
			if got := !w.bar(t, w.work).Stopped.IsZero(); got != c.fresh {
				t.Fatalf("stop on claude-work %v, want %v: %+v", got, c.fresh, w.bar(t, w.work))
			}
		})
	}
}

func TestConcurrentFilingFilesOneItemAndClosingFindsAny(t *testing.T) {
	w := newWireWorld(t)
	if err := recordStop(w.state, w.def, *w.clock, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	ps, err := profile.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.l.noteLogin(w.state, ps, w.def, profileConversation, nil, *w.clock); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if items := w.items(t); len(items) != 1 {
		t.Fatalf("%d items", len(items))
	}
	if err = updateBar(w.state, w.def, func(b *loginBar) error { b.Item, b.Filing, b.Said = "", time.Time{}, ""; return nil }); err != nil {
		t.Fatal(err)
	}
	if err = w.l.noteLogin(w.state, ps, w.def, profileConversation, nil, *w.clock); err != nil {
		t.Fatal(err)
	}
	if err = w.l.closeLoginItem(w.state, w.def, "usable"); err != nil {
		t.Fatal(err)
	}
	for _, it := range w.items(t) {
		if it.State == core.ItemOpen {
			t.Fatalf("an orphan stayed open: %+v", it)
		}
	}
}

// A login the owner names takes that one hit with a warning, as ym switch does, and the stop it meets moves the session on.
func TestWireNamingABarredLoginWarnsAndAStopMovesItOn(t *testing.T) {
	w := newWireWorld(t)
	if err := recordStop(w.state, w.def, *w.clock, w.clock.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	w.screen(t, w.def, wireDialog(t), "waiting")
	dir := filepath.Join(w.state, "fresh")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	started, err := w.l.Start(core.NewLaunch{Harness: "claude", Directory: dir, Slug: "named", Prompt: "go", Profile: "default"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(started.Message, "login default is out of its subscription limit until") {
		t.Fatalf("no warning: %q", started.Message)
	}
	if rec, ok := w.l.records.Find(started.Key); !ok || rec.Profile != "default" {
		t.Fatalf("the login the owner named was not used: %+v", rec)
	}
	unnamed, err := w.l.selectProfile("claude", "")
	if err != nil || unnamed.ID == "default" {
		t.Fatalf("an unnamed launch took the barred login: %+v, %v", unnamed, err)
	}
}

// Until the meter has finished a scan a login with no row may be walled, so nothing is moved anywhere. A session resuming where it was is no such
// choice, and a scan that ends in an error ends the wait as well.
func TestWireTheFirstPassBeforeRowsAreReadMovesNothing(t *testing.T) {
	setup := func(t *testing.T) (*wireWorld, func(), *int) {
		w := newWireWorld(t)
		w.addCodex(t)
		ports := countPorts(w)
		w.exhaust(t, w.other)
		unread := usage.New(w.state)
		t.Cleanup(func() { unread.Close() })
		scanned := false
		w.c.Meter = idleMeter{unread, &scanned}
		return w, func() {
			unread.Scan(time.Now())
			scanned = true
		}, ports
	}
	pass := func(t *testing.T, w *wireWorld) {
		t.Helper()
		*w.clock = w.clock.Add(time.Minute)
		b, err := w.c.Board()
		if err != nil {
			t.Fatal(err)
		}
		if err = errors.Join(w.l.RecoverProfiles(b), w.l.Failover(b), w.c.ReviveWork(b)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("a stop on screen waits for the rows", func(t *testing.T) {
		w, scan, ports := setup(t)
		w.screen(t, w.def, wireDialog(t), "waiting")
		w.land(t)
		pass(t, w)
		if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" || *ports != 0 {
			t.Fatalf("moved before any row was read: on %q, %d ports", rec.Profile, *ports)
		}
		scan()
		pass(t, w)
		if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" {
			t.Fatalf("not moved once rows were read: on %q, row %q", rec.Profile, w.rowText(t))
		}
	})
	t.Run("a scan that errors still ends the wait", func(t *testing.T) {
		w, scan, ports := setup(t)
		w.screen(t, w.def, wireDialog(t), "waiting")
		w.land(t)
		if err := os.Chmod(w.transcript(t, w.def), 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(w.transcript(t, w.def), 0o600) })
		scan()
		if err := os.Chmod(w.transcript(t, w.def), 0o600); err != nil {
			t.Fatal(err)
		}
		pass(t, w)
		if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" || *ports != 0 {
			t.Fatalf("frozen by a scan error: on %q, %d ports", rec.Profile, *ports)
		}
	})
	t.Run("a session ym's tmux lost resumes where it was", func(t *testing.T) {
		w, _, ports := setup(t)
		rec, _ := w.l.records.Find(w.rec.Key)
		rec.Running = true
		if err := w.l.records.Save(rec); err != nil {
			t.Fatal(err)
		}
		pass(t, w)
		pass(t, w)
		if got := strings.Join(w.starts(t), " "); got != "default" || *ports != 0 {
			t.Fatalf("starts %q, %d ports", got, *ports)
		}
	})
	t.Run("a dead manager keeps its own conversation", func(t *testing.T) {
		w, _, _ := setup(t)
		mgr, err := w.c.Store.AddManager(core.Manager{Role: core.RoleManager, Harness: "claude", Profile: "default", ConfigRoot: w.def.Root})
		if err != nil {
			t.Fatal(err)
		}
		mgr.Session = profileConversation
		if err = w.c.Store.SaveManager(mgr); err != nil {
			t.Fatal(err)
		}
		*w.clock = w.clock.Add(time.Minute)
		b, err := w.c.Board()
		if err != nil {
			t.Fatal(err)
		}
		if err = w.c.Loop(b); err != nil {
			t.Log(err)
		}
		saved, err := w.c.Store.Manager(mgr.ID)
		if err != nil || saved.Session != profileConversation {
			t.Fatalf("manager %+v, %v", saved, err)
		}
	})
}

// A port is made in one place, and every automatic way to it finds the gate there: with a login of the harness usable, none is made.
func TestWireThePortGateIsAtTheSink(t *testing.T) {
	queued := func(t *testing.T, w *wireWorld, state string) {
		t.Helper()
		ps, err := profile.Load(w.state)
		if err != nil {
			t.Fatal(err)
		}
		cx, err := profile.Find(ps, "codex", "codex-main")
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := w.l.records.Find(w.rec.Key)
		if err = saveMigration(w.state, migration{Version: 1, Source: rec, Destination: cx, State: state, Since: *w.clock}); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name  string
		run   func(*testing.T, *wireWorld)
		lands bool
	}{
		{name: "an exited session holding a queued port, ym revive", lands: true, run: func(t *testing.T, w *wireWorld) {
			queued(t, w, "queued")
			rec, _ := w.l.records.Find(w.rec.Key)
			if _, err := w.l.Revive(rec.Key); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an exited session holding a queued port, ReviveWork", lands: true, run: func(t *testing.T, w *wireWorld) {
			queued(t, w, "queued")
			rec, _ := w.l.records.Find(w.rec.Key)
			rec.Revive = true
			if err := w.l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			*w.clock = w.clock.Add(time.Minute)
			b, err := w.c.Board()
			if err != nil {
				t.Fatal(err)
			}
			if err = w.c.ReviveWork(b); err != nil {
				t.Log(err)
			}
		}},
		{name: "an unproven stop over a pending port", run: func(t *testing.T, w *wireWorld) {
			putProfileFile(t, w.transcript(t, w.def), fmt.Sprintf(`{"type":"user","sessionId":"%s","timestamp":"%s","message":{"role":"user","content":"go"}}`+"\n", profileConversation, w.clock.UTC().Format(time.RFC3339Nano)))
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.land(t)
			queued(t, w, "queued")
			for range 2 {
				w.pass(t, time.Minute)
			}
		}},
		{name: "stopped, then the write failed", lands: true, run: func(t *testing.T, w *wireWorld) {
			queued(t, w, "prepared")
			for range 3 {
				w.pass(t, time.Minute)
			}
		}},
		{name: "called directly", run: func(t *testing.T, w *wireWorld) {
			if err := recordStop(w.state, w.work, *w.clock, time.Time{}, nil); err != nil {
				t.Fatal(err)
			}
			queued(t, w, "queued")
			m, _ := loadMigration(w.state, "claude", profileConversation)
			rec, _ := w.l.records.Find(w.rec.Key)
			if _, err := w.l.portRecord(rec, core.Live{}, m); !errors.Is(err, errPortHeld) {
				t.Fatalf("portRecord: %v", err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.addCodex(t)
			ports := countPorts(w)
			w.exhaust(t, w.def, w.other)
			w.row(t, w.work, -1, 10, w.clock.Add(24*time.Hour))
			c.run(t, w)
			if *ports != 0 {
				t.Fatalf("%d ports", *ports)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); c.lands && rec.Profile != "claude-work" {
				t.Fatalf("on %q", rec.Profile)
			}
		})
	}
}

// Row and item name the login that holds a port and say what unblocks it, and neither recommends what the gate would refuse.
func TestWireRowAndItemSayWhatHoldsAPortAndWhatUnblocksIt(t *testing.T) {
	put := func(file, body string) func(*testing.T, *wireWorld) {
		return func(t *testing.T, w *wireWorld) { putProfileFile(t, filepath.Join(w.work.Root, file), body) }
	}
	for _, c := range []struct {
		name, unblocks string
		setup          func(*testing.T, *wireWorld)
	}{
		{name: "a check that hangs", unblocks: "ym profiles login claude claude-work", setup: put("hang", "")},
		{name: "a status that fails", unblocks: "ym profiles login claude claude-work", setup: put("fail", "")},
		{name: "a truncated identity", unblocks: "ym profiles login claude claude-work", setup: put(".claude.json", "{")},
		{name: "truncated settings", unblocks: "ym profiles login claude claude-work", setup: put("settings.json", "{")},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.addCodex(t)
			ports := countPorts(w)
			w.exhaust(t, w.other)
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.land(t)
			c.setup(t, w)
			for range 4 {
				w.pass(t, 20*time.Second)
			}
			row := w.rowText(t)
			if !strings.Contains(row, "claude-work") || !strings.Contains(row, c.unblocks) {
				t.Fatalf("row %q", row)
			}
			items := w.items(t)
			if len(items) != 1 || !strings.Contains(items[0].Rec, "claude-work") || !strings.Contains(items[0].Rec, c.unblocks) || strings.Contains(items[0].Rec, "move to codex") {
				t.Fatalf("items rec %q", items[0].Rec)
			}
			if *ports != 0 {
				t.Fatalf("%d ports", *ports)
			}
		})
	}
}

// One stop is one stop, however many ways ym hears of it across passes that do and do not look.
func TestWireOneStopSeenByScreenAndLedgerIsRecordedOnce(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute)))
	text, reset := w.walled(t, "walled-a-first-landing.txt")
	w.screen(t, w.def, text, "idle")
	w.row(t, w.def, -1, 100, reset)
	w.land(t)
	w.scan(t)
	w.pass(t, 15*time.Second)
	first := w.bar(t, w.def)
	hosts := w.c.Hosts
	for i := range 8 {
		w.c.Hosts = hosts
		if i%2 == 1 {
			w.c.Hosts = nil
		}
		w.pass(t, 15*time.Second)
	}
	w.c.Hosts = hosts
	if got := w.bar(t, w.def); !got.Stopped.Equal(first.Stopped) || got.Held || !got.Unclaimed {
		t.Fatalf("one stop re-recorded: %+v after %+v", got, first)
	}
}

// A session ym stopped to continue and could not start again comes back without anyone asking, unless the owner has since killed it.
func TestWireAReturningSessionAlreadyBackUsesNoLanding(t *testing.T) {
	w := newWireWorld(t)
	w.land(t)
	a, _ := loadFailover(w.state, "claude", profileConversation)
	a.Returning, a.State.Session = true, profileConversation
	if err := saveFailover(w.state, "claude", a); err != nil {
		t.Fatal(err)
	}
	w.pass(t, time.Minute)
	if a, _ = loadFailover(w.state, "claude", profileConversation); a.Returning || len(w.starts(t)) != 1 {
		t.Fatalf("returning %v, starts %q", a.Returning, w.starts(t))
	}
}

func TestWireASessionStoppedThenFailedComesBack(t *testing.T) {
	for _, c := range []struct {
		name  string
		owner bool
	}{{name: "by itself"}, {name: "not after the owner kills it", owner: true}} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.land(t)
			w.pass(t, time.Minute)
			*w.clock = w.bar(t, w.def).Until
			path := os.Getenv("PATH")
			w.c.Signal = func(pid int, sig syscall.Signal) error {
				os.Setenv("PATH", t.TempDir()+":/usr/bin:/bin")
				return syscall.Kill(pid, sig)
			}
			w.landed = false
			w.pass(t, time.Minute)
			os.Setenv("PATH", path)
			if got := len(w.starts(t)); got != 1 {
				t.Fatalf("%d landings before the retry", got)
			}
			if c.owner {
				if err := w.c.Store.RecordKill(profileConversation, *w.clock); err != nil {
					t.Fatal(err)
				}
			}
			w.pass(t, time.Minute)
			time.Sleep(500 * time.Millisecond)
			want := 2
			if c.owner {
				want = 1
			}
			if got := len(w.starts(t)); got != want {
				t.Fatalf("%d landings, want %d", got, want)
			}
		})
	}
}

// Esc leaves a stop standing, but not over a turn that has finished since or something being typed.
func TestWireAStandingStopGivesWayToATurnAndADraft(t *testing.T) {
	for _, c := range []struct {
		name  string
		then  func(*testing.T, *wireWorld, string)
		moves bool
	}{
		{name: "still at the prompt", moves: true, then: func(t *testing.T, w *wireWorld, text string) { w.screen(t, w.def, text, "idle") }},
		{name: "a draft in the box", then: func(t *testing.T, w *wireWorld, _ string) {
			rule := strings.Repeat("─", 40)
			w.screen(t, w.def, "✻ Worked for 1s\n"+rule+"\n❯ half a thought\n"+rule+"\n", "idle")
		}},
		{name: "a turn finished since", then: func(t *testing.T, w *wireWorld, text string) {
			w.screen(t, w.def, text, "idle")
			putProfileFile(t, w.transcript(t, w.def), wireTurn(profileConversation, w.clock.Add(time.Hour)))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.def, w.work, w.other)
			dialog, _ := w.walled(t, "walled-c-after-esc.txt")
			w.screen(t, w.def, dialog, "waiting")
			w.land(t)
			w.pass(t, time.Minute)
			after, _ := w.walled(t, "walled-d-esc-dialog.txt")
			c.then(t, w, after)
			time.Sleep(300 * time.Millisecond)
			w.pass(t, time.Minute)
			w.row(t, w.work, -1, 10, w.clock.Add(24*time.Hour))
			for range 2 {
				w.pass(t, time.Minute)
			}
			if rec, _ := w.l.records.Find(w.rec.Key); (rec.Profile == "claude-work") != c.moves {
				t.Fatalf("on %q, row %q", rec.Profile, w.rowText(t))
			}
		})
	}
}

func TestABadIdentityReadClosesNothing(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.work, w.other)
	w.screen(t, w.def, wireDialog(t), "waiting")
	w.land(t)
	w.pass(t, time.Minute)
	first := w.items(t)
	if len(first) != 1 {
		t.Fatalf("items %+v", first)
	}
	raw, err := os.ReadFile(filepath.Join(w.def.Root, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	putProfileFile(t, filepath.Join(w.def.Root, ".claude.json"), "{")
	w.pass(t, time.Minute)
	putProfileFile(t, filepath.Join(w.def.Root, ".claude.json"), string(raw))
	if it, err := w.c.Store.Item(first[0].Key()); err != nil || it.State != core.ItemOpen {
		t.Fatalf("item %+v, %v", it, err)
	}
}

// idleMeter is the real meter with no scan started but the one the test asks for.
type idleMeter struct {
	*usage.Meter
	scanned *bool
}

func (m idleMeter) Fleet(now time.Time) core.Fleet {
	if !*m.scanned {
		return core.Fleet{At: now}
	}
	return m.Meter.Fleet(now)
}

// A status that always exits 1 with no answer says nothing: the session stays, one item stands, and serve does not spawn a check per pass.
func TestWireAStatusThatAlwaysFailsIsAskedABoundedNumberOfTimes(t *testing.T) {
	w := newWireWorld(t)
	w.exhaust(t, w.other)
	w.screen(t, w.def, wireDialog(t), "waiting")
	w.land(t)
	putProfileFile(t, filepath.Join(w.work.Root, "fail"), "")
	if err := os.Remove(filepath.Join(w.work.Root, "status-calls")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	started := time.Now()
	for range 12 {
		w.pass(t, 15*time.Second)
	}
	raw, _ := os.ReadFile(filepath.Join(w.work.Root, "status-calls"))
	if got, most := strings.Count(string(raw), "x"), 1+int(time.Since(started)/statusFresh); got == 0 || got > most {
		t.Fatalf("%d status checks in 12 passes, want at most %d", got, most)
	}
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "default" {
		t.Fatalf("on %q", rec.Profile)
	}
	if items := w.items(t); len(items) != 1 {
		t.Fatalf("items %+v", items)
	}
	if a, _ := loadFailover(w.state, "claude", profileConversation); a.Failed[w.work.Account].After(*w.clock) {
		t.Fatalf("no answer held the login back: %+v", a.Failed)
	}
}

// Through the real queue: a port ym queued while the session was busy, once a login of its harness is usable, is never made.
func TestWireAPortQueuedWhileBusyIsNotMadeOnceALoginIsUsable(t *testing.T) {
	for _, route := range []string{"failover", "a queued switch whose login ran out"} {
		t.Run(route, func(t *testing.T) {
			w := newWireWorld(t)
			w.addCodex(t)
			ports := countPorts(w)
			w.exhaust(t, w.def, w.work, w.other)
			w.screen(t, w.def, wireIdle, "busy")
			w.land(t)
			rec, _ := w.l.records.Find(w.rec.Key)
			if route == "failover" {
				f, err := os.OpenFile(w.transcript(t, w.def), os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				fmt.Fprint(f, wireWall(time.Now()))
				f.Close()
				w.scan(t)
			} else {
				if err := saveMigration(w.state, migration{Version: 1, Source: rec, Destination: w.other, State: "queued", Since: *w.clock}); err != nil {
					t.Fatal(err)
				}
				if err := saveFailover(w.state, "claude", failoverAttempt{Event: "e", On: w.def.Account, State: core.FailoverState{Session: profileConversation, Profile: "default", Account: w.def.Account, Status: "queued", Target: w.other.ID}}); err != nil {
					t.Fatal(err)
				}
			}
			w.pass(t, time.Minute)
			if m, _ := loadMigration(w.state, "claude", profileConversation); route == "failover" && (m.State != "queued" || m.Destination.Harness != "codex") {
				a, _ := loadFailover(w.state, "claude", profileConversation)
				b, _ := w.c.Board()
				live, _ := b.Session(profileConversation)
				t.Fatalf("no port queued while busy: %+v; failover %+v; status %v exhaustions %+v", m.Destination.ID, a.State, live.Status, b.Fleet.Exhaustions)
			}
			w.row(t, w.work, -1, 10, w.clock.Add(24*time.Hour))
			putProfileFile(t, w.transcript(t, w.def), wireTurn(profileConversation, time.Now()))
			w.screen(t, w.def, wireIdle, "idle")
			time.Sleep(300 * time.Millisecond)
			for range 3 {
				w.pass(t, time.Minute)
			}
			if rec, _ = w.l.records.Find(w.rec.Key); *ports != 0 || rec.Profile != "claude-work" {
				t.Fatalf("%d ports, on %q", *ports, rec.Profile)
			}
		})
	}
}

// An owner's port goes through core.Switch, around the gate, however usable a login of the harness is.
func TestWireAnOwnersPortDoesNotMeetTheGate(t *testing.T) {
	w := newWireWorld(t)
	w.addCodex(t)
	ports := countPorts(w)
	w.row(t, w.work, -1, 10, w.clock.Add(24*time.Hour))
	w.land(t)
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := b.Session(profileConversation)
	w.c.Switch(b, s, "codex")
	if *ports != 1 {
		t.Fatalf("%d ports", *ports)
	}
}

// A conversation whose own login has recovered never ports: it goes back to that login.
func TestWireOwnLoginRecoveredMeansNoPort(t *testing.T) {
	t.Run("a normal turn since the stop", func(t *testing.T) {
		w := newWireWorld(t)
		w.addCodex(t)
		ports := countPorts(w)
		w.exhaust(t, w.work, w.other)
		if err := recordStop(w.state, w.def, *w.clock, time.Time{}, nil); err != nil {
			t.Fatal(err)
		}
		if err := saveFailover(w.state, "claude", failoverAttempt{Recorded: "e", On: w.def.Account, LastAt: w.clock.Add(-time.Hour), State: core.FailoverState{Session: profileConversation}}); err != nil {
			t.Fatal(err)
		}
		rec, _ := w.l.records.Find(w.rec.Key)
		w.l.revive(rec)
		if *ports != 0 {
			t.Fatalf("%d ports", *ports)
		}
	})
	t.Run("a row clears its own login, at the sink", func(t *testing.T) {
		w := newWireWorld(t)
		w.addCodex(t)
		ports := countPorts(w)
		w.exhaust(t, w.work, w.other)
		if err := recordStop(w.state, w.def, *w.clock, w.clock.Add(time.Hour), nil); err != nil {
			t.Fatal(err)
		}
		*w.clock = w.clock.Add(time.Minute)
		w.row(t, w.def, -1, 5, w.clock.Add(24*time.Hour))
		*w.clock = w.clock.Add(time.Minute)
		ps, err := profile.Load(w.state)
		if err != nil {
			t.Fatal(err)
		}
		cx, err := profile.Find(ps, "codex", "codex-main")
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := w.l.records.Find(w.rec.Key)
		_, err = w.l.portRecord(rec, core.Live{}, migration{Version: 1, Source: rec, Destination: cx, State: "queued", Since: *w.clock})
		if !errors.Is(err, errPortHeld) || !strings.Contains(err.Error(), "its own login") || *ports != 0 {
			t.Fatalf("%v, %d ports", err, *ports)
		}
	})
	t.Run("a real stop, a real port stop, the write failed, its own login recovered", func(t *testing.T) {
		w := newWireWorld(t)
		w.addCodex(t)
		w.exhaust(t, w.work, w.other)
		writes := 0
		w.c.PreparePort = func(core.Session, string, string) (core.PortPlan, error) {
			return core.PortPlan{Close: func() {}, Write: func() (string, error) { writes++; return "", errors.New("cs failed") }}, nil
		}
		text, reset := w.walled(t, "walled-c-after-esc.txt")
		w.screen(t, w.def, text, "waiting")
		w.land(t)
		w.landed = false
		w.pass(t, time.Minute)
		rec, _ := w.l.records.Find(w.rec.Key)
		if m, _ := loadMigration(w.state, "claude", profileConversation); writes == 0 || m.State != "prepared" || rec.Killed.IsZero() {
			t.Fatalf("no stopped port: %d writes, %+v, killed %v", writes, m.State, rec.Killed)
		}
		w.screen(t, w.def, wireIdle, "idle")
		*w.clock = reset.Add(time.Minute)
		for range 3 {
			w.pass(t, 20*time.Second)
		}
		time.Sleep(300 * time.Millisecond)
		if got := strings.Join(w.starts(t), " "); got != "default default" || writes != 1 {
			t.Fatalf("starts %q, %d writes", got, writes)
		}
	})
	t.Run("an owner's kill after the port stopped it stays", func(t *testing.T) {
		w := newWireWorld(t)
		w.addCodex(t)
		w.exhaust(t, w.work, w.other)
		ps, err := profile.Load(w.state)
		if err != nil {
			t.Fatal(err)
		}
		cx, err := profile.Find(ps, "codex", "codex-main")
		if err != nil {
			t.Fatal(err)
		}
		rec, _ := w.l.records.Find(w.rec.Key)
		rec.Killed = w.clock.Add(time.Hour)
		if err = w.l.records.Save(rec); err != nil {
			t.Fatal(err)
		}
		if err = saveMigration(w.state, migration{Version: 1, Source: rec, Destination: cx, State: "prepared", Since: *w.clock}); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			w.pass(t, time.Minute)
		}
		if got := w.starts(t); len(got) != 0 {
			t.Fatalf("starts %q", got)
		}
	})
}

// A stopped session is never sent to a login that answered it is signed out.
func TestWireAStoppedPortIsNotRetargetedToASignedOutLogin(t *testing.T) {
	w := newWireWorld(t)
	w.addCodex(t)
	ports := countPorts(w)
	w.exhaust(t, w.def)
	putProfileFile(t, filepath.Join(w.work.Root, "out"), "")
	ps, err := profile.Load(w.state)
	if err != nil {
		t.Fatal(err)
	}
	cx, err := profile.Find(ps, "codex", "codex-main")
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := w.l.records.Find(w.rec.Key)
	if err = saveMigration(w.state, migration{Version: 1, Source: rec, Destination: cx, State: "prepared", Since: *w.clock}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		w.pass(t, time.Minute)
	}
	if rec, _ = w.l.records.Find(w.rec.Key); rec.Profile != "personal" || *ports != 0 {
		t.Fatalf("on %q, %d ports", rec.Profile, *ports)
	}
}

// A session that merely exited says on its row what holds it, and neither says what ym switch would refuse.
func TestWireAnExitedSessionSaysWhatHoldsIt(t *testing.T) {
	put := func(file, body string) func(*testing.T, *wireWorld) {
		return func(t *testing.T, w *wireWorld) { putProfileFile(t, filepath.Join(w.work.Root, file), body) }
	}
	for _, c := range []struct {
		name  string
		setup func(*testing.T, *wireWorld)
	}{
		{name: "a check that hangs", setup: put("hang", "")},
		{name: "a status that fails", setup: put("fail", "")},
		{name: "a truncated identity", setup: put(".claude.json", "{")},
		{name: "truncated settings", setup: put("settings.json", "{")},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.addCodex(t)
			ports := countPorts(w)
			w.exhaust(t, w.def, w.other)
			c.setup(t, w)
			rec, _ := w.l.records.Find(w.rec.Key)
			_, err := w.l.revive(rec)
			if err == nil || !strings.Contains(err.Error(), "claude-work") || strings.Contains(err.Error(), "ym switch") || *ports != 0 {
				t.Fatalf("%d ports, err %v", *ports, err)
			}
			*w.clock = w.clock.Add(time.Minute)
			b, berr := w.c.Board()
			if berr != nil {
				t.Fatal(berr)
			}
			found := ""
			for _, l := range b.Launched {
				if l.Session == profileConversation && l.Switching != nil {
					found = l.Switching.String()
				}
			}
			if !strings.Contains(found, "claude-work") || !strings.Contains(found, "ym profiles login") {
				t.Fatalf("row %q", found)
			}
		})
	}
}

// Fresh rows clear a false stop across a fleet of idle sessions, whichever of them a pass looks at; only one really showing a stop holds it.
func TestWireAFalseStopClearsAcrossAFleetUnlessOneShowsIt(t *testing.T) {
	for _, showing := range []bool{false, true} {
		t.Run(fmt.Sprintf("one showing the stop %v", showing), func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.work, w.other)
			w.screen(t, w.def, wireIdle, "idle")
			w.land(t)
			w.addSession(t, 1, w.def)
			w.pass(t, 20*time.Second)
			w.addSession(t, 2, w.def)
			w.pass(t, 20*time.Second)
			text, _ := w.walled(t, "walled-a-first-landing.txt")
			w.screen(t, w.def, text, "idle")
			time.Sleep(400 * time.Millisecond)
			w.pass(t, 20*time.Second)
			if !showing {
				w.screen(t, w.def, wireIdle, "idle")
				time.Sleep(400 * time.Millisecond)
			}
			for range 6 {
				w.pass(t, 20*time.Second)
				w.row(t, w.def, -1, 5, w.clock.Add(24*time.Hour))
			}
			w.pass(t, 20*time.Second)
			if got := !barredUntil(w.state, w.def, w.l.limits(), *w.clock).IsZero(); got != showing {
				t.Fatalf("barred %v, want %v: %+v", got, showing, w.bar(t, w.def))
			}
		})
	}
}

// Typing under the footer is the owner's: a session is not moved out from under it.
func TestWireADraftUnderALiveFooterIsNotMoved(t *testing.T) {
	rule := strings.Repeat("─", 40)
	footer := "  ⚠ Usage limit reached · limit resets 3am\n    Continuing automatically at 3am · esc to cancel\n"
	for _, c := range []struct {
		name, box string
		moves     bool
	}{{"an empty box", "❯ ", true}, {"a draft", "❯ half a thought", false}} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.screen(t, w.def, "✻ Worked for 1s\n"+rule+"\n"+c.box+"\n"+rule+"\n"+footer, "idle")
			w.land(t)
			w.pass(t, time.Minute)
			if rec, _ := w.l.records.Find(w.rec.Key); (rec.Profile == "claude-work") != c.moves {
				t.Fatalf("on %q", rec.Profile)
			}
			if !c.moves && !strings.Contains(w.rowText(t), "a draft in its input box") {
				t.Fatalf("a silent hold: row %q", w.rowText(t))
			}
		})
	}
}

// A ledger stop lasts for ever: one a session left behind is history and costs no file, a recent one or one on the login it is on still counts.
func TestWireAHistoricLedgerStopIsLeftAlone(t *testing.T) {
	for _, c := range []struct {
		name     string
		age      time.Duration
		recorded bool
	}{
		{name: "days old, on a login it has left", age: 72 * time.Hour},
		{name: "minutes old", age: 10 * time.Minute, recorded: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWireWorld(t)
			w.exhaust(t, w.def, w.work, w.other)
			b, err := w.c.Board()
			if err != nil {
				t.Fatal(err)
			}
			b.At = *w.clock
			b.Fleet.Exhaustions = []core.Exhaustion{{Harness: "claude", Profile: "default", Account: w.def.Account, Session: "55555555-5555-4555-8555-555555555555", Reason: quotaReason("claude"), Event: "old", Seen: w.clock.Add(-c.age)}}
			if err = w.l.Failover(b); err != nil {
				t.Fatal(err)
			}
			_, statErr := os.Stat(failoverPath(w.state, "claude", "55555555-5555-4555-8555-555555555555"))
			if (statErr == nil) != c.recorded {
				t.Fatalf("file %v, want recorded %v", statErr, c.recorded)
			}
		})
	}
}

// restartServe is a new ym over the same state, the panes left running: it knows only what it saved.
func (w *wireWorld) restartServe(t *testing.T) {
	t.Helper()
	c, err := Wire(filepath.Join(w.state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Store.Close() })
	m := usage.New(w.state)
	t.Cleanup(func() { m.Close() })
	c.Meter, w.meter = m, m
	if _, err = m.Scan(time.Now()); err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return *w.clock }
	w.l = c.Launches.(launcher)
	w.l.statuses = &statusChecks{}
	c.Launches = w.l
	w.c = c
}

// restartProcess ends the session's process and starts it again on the same login, past any gate, while its screen shows the same stop.
func (w *wireWorld) restartProcess(t *testing.T) {
	t.Helper()
	b, err := w.c.Board()
	if err != nil {
		t.Fatal(err)
	}
	s, _ := b.Session(profileConversation)
	if _, err = w.c.Stop(s); err != nil {
		t.Fatal(err)
	}
	once := w.l
	once.anyway, once.operation = true, profileConversation
	rec, _ := w.l.records.Find(w.rec.Key)
	rec.Killed = time.Time{}
	if err = w.l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err = w.c.Store.ClearKill(profileConversation); err != nil {
		t.Fatal(err)
	}
	before := len(w.starts(t))
	if _, err = once.revive(rec); err != nil {
		t.Fatal(err)
	}
	w.settle(t, before+1)
}

// One stop is one stop across a restart of ym or of the session's process while its bar stands. A later turn's stop is a second, and so is a
// dialog that returns once the bar has passed, as that landing tried the login again.
func TestWireOneStopAcrossARestartIsOneStop(t *testing.T) {
	addTurn := func(t *testing.T, w *wireWorld) {
		f, err := os.OpenFile(w.transcript(t, w.def), os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(f, wireTurn(profileConversation, w.clock.Add(time.Second)))
		f.Close()
	}
	for _, known := range []bool{false, true} {
		for _, c := range []struct {
			name   string
			then   func(*testing.T, *wireWorld)
			second bool
		}{
			{name: "ym restarts with the pane alive", then: func(t *testing.T, w *wireWorld) { w.restartServe(t) }},
			{name: "its process restarts while the bar stands", then: func(t *testing.T, w *wireWorld) { w.restartProcess(t) }},
			{name: "ym and its process both restart", then: func(t *testing.T, w *wireWorld) {
				w.restartProcess(t)
				w.restartServe(t)
			}},
			{name: "a new turn stops in the same process", second: true, then: addTurn},
			{name: "a new turn stops after a restart", second: true, then: func(t *testing.T, w *wireWorld) {
				addTurn(t, w)
				w.restartProcess(t)
			}},
			{name: "the dialog returns once the bar has passed", second: true, then: func(t *testing.T, w *wireWorld) {
				*w.clock = w.bar(t, w.def).Until.Add(time.Minute)
				w.restartProcess(t)
			}},
		} {
			t.Run(fmt.Sprintf("%s/known reset %v", c.name, known), func(t *testing.T) {
				w := newWireWorld(t)
				w.exhaust(t, w.work, w.other)
				text := wireDialog(t)
				if known {
					text, _ = w.walled(t, "walled-c-after-esc.txt")
				}
				w.screen(t, w.def, text, "waiting")
				w.land(t)
				w.pass(t, time.Minute)
				first := w.bar(t, w.def)
				items := w.items(t)
				if len(items) != 1 || first.Stopped.IsZero() || first.Held {
					t.Fatalf("bar %+v, items %+v", first, items)
				}
				if _, err := w.c.Store.Transition(items[0].Key(), core.ItemDismissed, "not now"); err != nil {
					t.Fatal(err)
				}
				c.then(t, w)
				for range 2 {
					w.pass(t, 15*time.Second)
				}
				got := w.bar(t, w.def)
				if c.second {
					if !got.Stopped.After(first.Stopped) {
						t.Fatalf("a real second stop was not counted: %+v after %+v", got, first)
					}
					return
				}
				if !got.Stopped.Equal(first.Stopped) || got.Held || !got.Until.Equal(first.Until) || got.Unclaimed != first.Unclaimed || !got.Claim.Equal(first.Claim) {
					t.Fatalf("one stop counted twice: %+v after %+v", got, first)
				}
				if all := w.items(t); len(all) != 1 {
					t.Fatalf("a closed item was filed again: %d items", len(all))
				}
			})
		}
	}
}

// A delivery of ym's own caught in the box is no draft, so ym's typing never holds a session on the wall.
func TestWireADeliveryInTheBoxIsNoDraft(t *testing.T) {
	rule := strings.Repeat("─", 40)
	footer := "  ⚠ Usage limit reached · limit resets 3am\n    Continuing automatically at 3am · esc to cancel\n"
	w := newWireWorld(t)
	w.screen(t, w.def, "✻ Worked for 1s\n"+rule+"\n❯ ym: a delivery from your manager\n"+rule+"\n"+footer, "idle")
	w.land(t)
	if _, err := w.c.Store.AddMessage(profileConversation, "ym: a delivery from your manager", core.MsgUncertain); err != nil {
		t.Fatal(err)
	}
	w.pass(t, time.Minute)
	if rec, _ := w.l.records.Find(w.rec.Key); rec.Profile != "claude-work" {
		t.Fatalf("ym's own delivery held it: on %q, row %q", rec.Profile, w.rowText(t))
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
		if err = recordStop(w.state, p, now, now.Add(time.Hour), nil); err != nil {
			t.Fatal(err)
		}
	}
	dest := func() (profile.Profile, error) {
		ps, _ := profile.Load(w.state)
		return w.l.destination(w.state, failoverAttempt{}, ps, core.Exhaustion{Harness: "claude", Profile: "default", Account: w.def.Account}, w.cwd, nil, now)
	}
	if got, err := dest(); err != nil || got.Harness != "codex" {
		t.Fatalf("every claude login barred: %+v, %v", got, err)
	}
	if err = recordStop(w.state, cx, now, now.Add(time.Hour), nil); err != nil {
		t.Fatal(err)
	}
	if got, err := dest(); err == nil {
		t.Fatalf("every login of both harnesses barred, yet ported to %+v", got)
	}
}
