package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestAutomaticFailoverRequiresBoundActualSubscriptionExhaustion(t *testing.T) {
	now := time.Now()
	e := core.Exhaustion{Harness: "codex", Profile: "a", Account: "codex/u/a", Reason: "usage_limit_exceeded", Seen: now}
	window := core.Limit{Harness: "codex", Profile: "a", Account: e.Account, Name: "codex 5h", Used: 100, Seen: now, ResetsAt: now.Add(time.Hour)}
	for _, tc := range []struct {
		name                   string
		reason, account, scope string
		used                   float64
		recovered              bool
		want                   bool
	}{
		{"actual", "usage_limit_exceeded", e.Account, "codex 5h", 100, false, true},
		{"warning", "usage_limit_exceeded", e.Account, "codex 5h", 99, false, false},
		{"generic 429", "rate_limit_exceeded", e.Account, "codex 5h", 100, false, false},
		{"overloaded", "server_overloaded", e.Account, "codex 5h", 100, false, false},
		{"auth failure", "unauthorized", e.Account, "codex 5h", 100, false, false},
		{"permission model", "usage_limit_exceeded", e.Account, "permission-model 5h", 100, false, false},
		{"wrong account", "usage_limit_exceeded", "codex/u/b", "codex 5h", 100, false, false},
		{"unknown account", "usage_limit_exceeded", "", "codex 5h", 100, false, false},
		{"recovered turn", "usage_limit_exceeded", e.Account, "codex 5h", 100, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := e
			candidate.Reason = tc.reason
			candidate.Recovered = tc.recovered
			w := window
			w.Account = tc.account
			w.Name = tc.scope
			w.Used = tc.used
			_, got := trueExhaustion(candidate, []core.Limit{w}, now)
			if got != tc.want {
				t.Fatalf("actual=%v want=%v", got, tc.want)
			}
		})
	}
}
func TestAutomaticFailoverNeverUsesAnAliasUnknownOrAttemptedPool(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	e := core.Exhaustion{Harness: "claude", Account: "claude/u/a"}
	ps := []profile.Profile{{Harness: "claude", ID: "alias", Account: e.Account}, {Harness: "claude", ID: "unknown"}, {Harness: "codex", ID: "other", Account: "codex/u/c"}, {Harness: "claude", ID: "b", Account: "claude/u/b"}, {Harness: "claude", ID: "c", Account: "claude/u/c"}}
	a := failoverAttempt{State: core.FailoverState{Session: profileConversation}, Pools: map[string]time.Time{e.Account: now.Add(time.Hour), "claude/u/b": now.Add(time.Hour)}}
	if err := saveFailover(dir, "claude", a); err != nil {
		t.Fatal(err)
	}
	restarted, err := loadFailover(dir, "claude", profileConversation)
	if err != nil {
		t.Fatal(err)
	}
	target, err := failoverTarget(ps, e, nil, restarted.Pools, now)
	if err != nil || target.ID != "c" {
		t.Fatalf("target=%#v %v", target, err)
	}
	restarted.Pools[target.Account] = now.Add(time.Hour)
	if _, err = failoverTarget(ps, e, nil, restarted.Pools, now); err == nil {
		t.Fatal("pool loop permitted after restart")
	}
}
func TestNativeLifecycleRejectsPendingAndSharedResumersWithoutPublication(t *testing.T) {
	sourceRoot, destRoot, state := t.TempDir(), t.TempDir(), t.TempDir()
	rec := launch.Record{Key: "source", Harness: "codex", Session: profileConversation, Profile: "a", ConfigRoot: sourceRoot, NativePID: 100, Dedicated: true}
	other := launch.Record{Key: "unrelated", Harness: "codex", Session: "33333333-3333-4333-8333-333333333333", Profile: "a", ConfigRoot: sourceRoot, NativePID: 200, Dedicated: true}
	records := launch.Records{Dir: filepath.Join(state, "launches")}
	if err := records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err := records.Save(other); err != nil {
		t.Fatal(err)
	}
	ps := []proc.Proc{{PID: 100, Args: []string{"codex", "--no-daemon"}}, {PID: 101, PPID: 100, Args: []string{"codex", "app-server"}}, {PID: 200, Args: []string{"codex", "--no-daemon"}}, {PID: 201, PPID: 200, Args: []string{"codex", "app-server"}}}
	l := launcher{records: records, core: &core.Core{Procs: func() ([]proc.Proc, error) { return ps, nil }}, rootOf: func(pid int, key string) string {
		if key == "CODEX_HOME" {
			return sourceRoot
		}
		if pid == 100 || pid == 101 {
			return "source"
		}
		if pid == 200 || pid == 201 {
			return "unrelated"
		}
		return ""
	}}
	dest := profile.Profile{Harness: "codex", ID: "b", Root: destRoot}
	children, err := l.nativeLifecycle(rec, dest, true)
	if err != nil || len(children) != 2 {
		t.Fatalf("managed lifecycle: %v %v", children, err)
	}
	ps = append(ps, proc.Proc{PID: 300, Args: []string{"codex", "app-server"}})
	if _, err = l.nativeLifecycle(rec, dest, true); err == nil {
		t.Fatal("shared/preparing external native resumer was admitted")
	}
	if files, _ := os.ReadDir(destRoot); len(files) != 0 {
		t.Fatal("refusal published destination artifacts")
	}
	ps = ps[2:4]
	if _, err = l.nativeLifecycle(rec, dest, false); err != nil {
		t.Fatalf("drained source blocked unrelated managed session: %v", err)
	}
}
func TestExhaustionStatusIsVisibleAndSecretFree(t *testing.T) {
	w := usageWire(core.Fleet{Ready: true, Failovers: []core.FailoverState{{Session: profileConversation, Status: "blocked", Reason: "external/shared native owner"}}})
	b, err := json.Marshal(w)
	if err != nil {
		t.Fatal(err)
	}
	round := usageFromWire(w)
	if round == nil || len(round.Failovers) != 1 || round.Failovers[0].Status != "blocked" || len(b) == 0 {
		t.Fatal("blocked state lost over wire")
	}
}
