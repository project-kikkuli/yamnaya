package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type quotaHarness struct {
	switchingHarness
	at      time.Time
	problem error
}

type changingCopyHarness struct {
	quotaHarness
	marker string
}

func (h changingCopyHarness) Discover(ps []proc.Proc) []core.Session {
	out := h.switchingHarness.Discover(ps)
	for i := range out {
		out[i].Status = core.Idle
		if i > 0 {
			out[i].Status = core.Waiting
			if _, err := os.Stat(h.marker); err == nil {
				out[i].Status = core.Busy
			}
		}
	}
	return out
}

func TestSwitchRevalidatesWaitingSecondaryCopyBeforeStoppingIdlePrimary(t *testing.T) {
	state := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", state)
	a := profile.Profile{Harness: "claude", ID: "a", Root: t.TempDir(), Explicit: true}
	b := profile.Profile{Harness: "claude", ID: "b", Root: t.TempDir(), Explicit: true}
	for _, p := range []profile.Profile{a, b} {
		if err := profile.Add(state, p); err != nil {
			t.Fatal(err)
		}
	}
	a.Root, b.Root = physicalProfileRoot(a.Root), physicalProfileRoot(b.Root)
	subscriptionIdentity(t, &a)
	subscriptionIdentity(t, &b)
	cwd := t.TempDir()
	history := filepath.Join("projects", "p", profileConversation+".jsonl")
	putProfileFile(t, filepath.Join(a.Root, history), profileResponse("claude", time.Now().Format(time.RFC3339Nano), "first", 100, 20))
	marker := filepath.Join(t.TempDir(), "auth-checked")
	bin := filepath.Join(t.TempDir(), "claude")
	putProfileFile(t, bin, fmt.Sprintf("#!/bin/sh\n: > %s\nprintf '%%s' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\"}'\n", shellQuote(marker)))
	if err := os.Chmod(bin, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAMNAYA_CLAUDE_BIN", bin)
	st, err := core.OpenStore(filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	at := time.Now().Add(-time.Second)
	db, err := sql.Open("sqlite", filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE ym_usage_exhaustions(body TEXT);CREATE TABLE ym_usage_limits(body TEXT)`)
	if err != nil {
		t.Fatal(err)
	}
	e, _ := json.Marshal(core.Exhaustion{Harness: "claude", Profile: "a", Account: a.Account, Session: profileConversation, Reason: "subscription_rate_limit", Seen: at})
	w, _ := json.Marshal(core.Limit{Harness: "claude", Profile: "a", Account: a.Account, Name: "7d", Used: 100, Seen: time.Now(), ResetsAt: time.Now().Add(time.Hour)})
	if _, err = db.Exec(`INSERT INTO ym_usage_exhaustions VALUES(?)`, string(e)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO ym_usage_limits VALUES(?)`, string(w)); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s := core.Session{ID: profileConversation, Harness: "claude", Profile: "a", Account: a.Account, ConfigRoot: a.Root, Cwd: cwd, Status: core.Idle}
	h := changingCopyHarness{quotaHarness: quotaHarness{switchingHarness: switchingHarness{session: s}, at: at}, marker: marker}
	l := testLauncher(t, state)
	signals := 0
	l.core = &core.Core{Store: st, StateDir: state, Harnesses: []core.Harness{h}, Hosts: []core.Host{quotaHost{text: "quota dialog"}}, Procs: func() ([]proc.Proc, error) {
		return []proc.Proc{{PID: 42, TTY: "/one", Args: []string{"claude"}}, {PID: 43, TTY: "/two", Args: []string{"claude"}}}, nil
	}, Signal: func(int, syscall.Signal) error { signals++; return nil }}
	rec := launch.Record{Key: "conversation", Harness: "claude", Session: s.ID, Worktree: launch.Worktree{Path: cwd}, Adopted: true, AttachedAt: at.Add(-time.Second)}
	setRecordProfile(&rec, a)
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	_, err = l.SwitchProfile(rec.Key, "b")
	if err == nil || signals != 0 {
		t.Fatalf("resumed copy interrupted: %v signals=%d", err, signals)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("did not reach authentication between eligibility and revalidation")
	}
	saved, _ := l.records.Find(rec.Key)
	if saved.Profile != "a" {
		t.Fatalf("source changed: %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(b.Root, history)); !os.IsNotExist(err) {
		t.Fatal("published active-copy conversation")
	}
}

func (h quotaHarness) InspectHandoff(core.Session) (time.Time, error) { return h.at, h.problem }
func (h quotaHarness) QuotaDialog(text string) bool                   { return text == "quota dialog" }

type quotaHost struct {
	text string
	err  error
}

func (h quotaHost) Name() string { return "fixture" }
func (h quotaHost) Locate(tty string) (core.Handle, bool, error) {
	return core.Handle{Host: "fixture", ID: tty}, true, nil
}
func (h quotaHost) Focus(core.Handle) (string, error)  { return "", nil }
func (h quotaHost) Paste(core.Handle, string) error    { return nil }
func (h quotaHost) Enter(core.Handle) error            { return nil }
func (h quotaHost) Screen(core.Handle) (string, error) { return h.text, h.err }

func TestQuotaSwitchEligibilityBindsMainProofAccountAndEveryCopy(t *testing.T) {
	for _, name := range []string{"quota stopped", "later child ledger", "idle", "busy", "unknown", "prompt", "unreadable screen", "unknown screen", "active main", "active child", "active copy", "waiting secondary copy", "resumed into exhausted login", "stale ledger", "recovered", "wrong account", "missing account", "wrong profile", "wrong session", "wrong harness", "reset window", "not exhausted", "source root rebound", "source account changed", "copy profile changed", "copy account changed", "copy root changed", "copy harness changed", "copy session changed", "canonical source alias"} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			at := now.Add(-time.Minute)
			s := core.Live{Session: core.Session{ID: "session", Harness: "claude", Profile: "work", Account: "claude/user/work", ConfigRoot: "/source", Status: core.Waiting, PID: 42, TTY: "/fixture"}}
			rec := launch.Record{Session: s.ID, Harness: s.Harness, Profile: s.Profile, Account: s.Account, ConfigRoot: s.ConfigRoot, AttachedAt: at.Add(-time.Minute)}
			e := core.Exhaustion{Harness: s.Harness, Profile: s.Profile, Session: s.ID, Account: s.Account, Reason: "subscription_rate_limit", Seen: at}
			window := core.Limit{Harness: s.Harness, Profile: s.Profile, Account: s.Account, Name: "7d", Used: 100, Seen: now, ResetsAt: now.Add(time.Hour)}
			h := quotaHarness{switchingHarness: switchingHarness{session: s.Session}, at: at}
			host := quotaHost{text: "quota dialog"}
			want := false
			switch name {
			case "quota stopped", "idle", "active child", "waiting secondary copy", "later child ledger", "canonical source alias", "resumed into exhausted login":
				want = true
			}
			switch name {
			case "later child ledger":
				e.Seen = at.Add(5 * time.Second)
			case "idle":
				s.Status = core.Idle
			case "busy":
				s.Status = core.Busy
			case "unknown":
				s.Status = core.Unknown
			case "prompt":
				s.Prompt = &core.Prompt{Tool: "Bash"}
			case "unreadable screen":
				host.err = errors.New("unreadable")
			case "unknown screen":
				host.text = "ordinary dialog"
			case "active main":
				h.problem = errors.New("outstanding tool")
			case "active child":
				s.Crew = []core.Member{{Status: core.Busy}}
			case "active copy":
				cp := s.Session
				cp.Status = core.Busy
				s.Copies = []core.Session{cp}
			case "waiting secondary copy":
				s.Copies = []core.Session{s.Session}
				s.Status = core.Idle
			case "resumed into exhausted login":
				h.at = now.Add(-25 * time.Hour)
				e.Seen = h.at
				rec.AttachedAt = now.Add(-11 * time.Hour)
				window.ResetsAt = now.Add(32 * time.Hour)
			case "stale ledger":
				e.Seen = at.Add(-time.Second)
			case "recovered":
				e.Recovered = true
			case "wrong account":
				e.Account = "other"
			case "missing account":
				s.Account = ""
				rec.Account = ""
			case "wrong profile":
				e.Profile = "other"
			case "wrong session":
				e.Session = "other"
			case "wrong harness":
				e.Harness = "codex"
			case "reset window":
				window.ResetsAt = at
			case "not exhausted":
				window.Used = 99
			case "source root rebound":
				rec.ConfigRoot = "/old-root"
				s.Status = core.Idle
			case "source account changed":
				rec.Account = "old-account"
				s.Status = core.Idle
			case "copy profile changed":
				cp := s.Session
				cp.Profile = "other"
				cp.Status = core.Idle
				s.Copies = []core.Session{cp}
			case "copy account changed", "copy root changed", "copy harness changed", "copy session changed":
				cp := s.Session
				cp.Status = core.Idle
				switch name {
				case "copy account changed":
					cp.Account = "other"
				case "copy root changed":
					cp.ConfigRoot = "/other"
				case "copy harness changed":
					cp.Harness = "codex"
				case "copy session changed":
					cp.ID = "other"
				}
				s.Copies = []core.Session{cp}
			case "canonical source alias":
				root := t.TempDir()
				alias := filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(root, alias); err != nil {
					t.Fatal(err)
				}
				rec.ConfigRoot = root
				s.ConfigRoot = alias
			}
			l := launcher{core: &core.Core{Harnesses: []core.Harness{h}, Hosts: []core.Host{host}}}
			f := core.Fleet{At: now, Exhaustions: []core.Exhaustion{e}, Limits: []core.Limit{window}}
			if err := l.switchReady(s, rec, &f); (err == nil) != want {
				t.Fatalf("ready=%v want=%v", err, want)
			}
		})
	}
}
