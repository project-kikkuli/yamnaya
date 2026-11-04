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
	landed           bool
}

const wireIdle = "✻ Done\n────────────────\n❯ \n────────────────\n"

const wireClaude = `#!/bin/bash
case " $* " in *" auth status "*|*" login status "*) [ -f "$CLAUDE_CONFIG_DIR/hang" ] && sleep 30; printf '%s\n' '{"loggedIn":true,"authMethod":"claude.ai"}'; exit 0;; esac
root="$CLAUDE_CONFIG_DIR"
prev=""; for a in "$@"; do [ "$prev" = --resume ] && id="$a"; [ "$prev" = --session-id ] && id="$a"; prev="$a"; done
printf '%s\n' "$YAMNAYA_PROFILE" >> "$WIRE_LOG"
printf '%s %s %s\n' "$YAMNAYA_PROFILE" "$CLAUDE_SECURESTORAGE_CONFIG_DIR" "$root" >> "$WIRE_LOG.env"
mkdir -p "$root/sessions"
printf '{"pid":%d,"sessionId":"%s","cwd":"%s","status":"%s","statusUpdatedAt":%d}' $$ "$id" "$PWD" "$(cat "$root/status")" "$(date +%s%3N)" > "$root/sessions/$$.json"
[ -f "$root/child" ] && { setsid bash -c 'exec -a claude sleep 100000' </dev/null >/dev/null 2>&1 & }
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
			w.screen(t, w.def, wireDialog(t), "waiting")
			w.row(t, w.def, c.five, c.seven, w.clock.Add(24*time.Hour))
			w.land(t)
			want := 1 + c.lifts
			for range 12 {
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
			for range 12 {
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
	w.pass(t, 2*time.Minute)
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
	w.screen(t, w.def, wireDialog(t), "waiting")
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
	if len(items) != 1 || !strings.HasPrefix(items[0].Rec, "switch to ") || !strings.Contains(items[0].Rec, "has not answered") || strings.Contains(items[0].Rec, "usage left") {
		t.Fatalf("the item says what it cannot know of a hung login: %+v", items)
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
	if _, err := w.c.Meter.(*usage.Meter).Scan(time.Now()); err != nil {
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
			if got := len(w.starts(t)); got > 2 {
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
	putProfileFile(t, w.transcript(t, w.def), wireWall(w.clock.Add(-time.Minute))+wireWall(*w.clock))
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
			bins := strings.SplitN(os.Getenv("PATH"), ":", 2)[0]
			if err := os.WriteFile(filepath.Join(bins, "codex"), []byte(wireClaude), 0o700); err != nil {
				t.Fatal(err)
			}
			cx := profile.Profile{Harness: "codex", ID: "codex-main", Root: t.TempDir(), Explicit: true}
			if err := profile.Add(w.state, cx); err != nil {
				t.Fatal(err)
			}
			cx.Root = physicalProfileRoot(cx.Root)
			subscriptionIdentity(t, &cx)
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
	for _, id := range []string{"default", "claude-work"} {
		t.Run(id, func(t *testing.T) {
			w := newWireWorld(t)
			t.Setenv("CLAUDE_SECURESTORAGE_CONFIG_DIR", "/wrong/credentials")
			if id != "default" {
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
		w.l.silentStops(b)
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
		if err = recordStop(w.state, p, now, time.Time{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	dest := func() (profile.Profile, error) {
		ps, _ := profile.Load(w.state)
		return failoverDestination(usableLogins(w.state, failoverAttempt{}, ps, nil, now), core.Exhaustion{Harness: "claude", Account: w.def.Account}, w.cwd)
	}
	if got, err := dest(); err != nil || got.Harness != "codex" {
		t.Fatalf("every claude login barred: %+v, %v", got, err)
	}
	if err = recordStop(w.state, cx, now, time.Time{}, nil); err != nil {
		t.Fatal(err)
	}
	if got, err := dest(); err == nil {
		t.Fatalf("every login of both harnesses barred, yet ported to %+v", got)
	}
}
