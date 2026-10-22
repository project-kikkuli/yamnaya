package cli

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

func handoffWorld(t *testing.T) (*core.Core, string) {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", "git@github.com:example/query.git"}} {
		if raw, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", raw, err)
		}
	}
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return &core.Core{Store: s}, dir
}

// underManager makes session "lead" an agent of a manager running as session "boss".
func underManager(t *testing.T, c *core.Core) core.Manager {
	t.Helper()
	g, err := c.Store.AddGoal(core.Goal{Ask: "ship the query cache"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.Store.AddManager(core.Manager{Role: core.RoleManager, Name: "cache", GoalID: g.ID})
	if err != nil {
		t.Fatal(err)
	}
	m.Session = "boss"
	if err = c.Store.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	if err = c.Store.Link("lead", "boss"); err != nil {
		t.Fatal(err)
	}
	return m
}

func handoffClaim(dir, id, kind, target, text string) handoff.Claim {
	return handoff.Claim{ID: id, Event: handoff.Event{ID: id, Work: "lead", Session: "lead", Agent: "shepherd", Harness: "claude", Path: "/tmp/agent.jsonl", Dir: dir, Text: text}, Kind: kind, Target: target, Topic: kind + " topic", Question: "Act on it", Quote: text}
}

func TestEveryHandoffKindFromAManagedAgentFilesOneItemOnceWithItsMessage(t *testing.T) {
	for _, tc := range []struct {
		kind, target, text, wantKind, wantAction, wantInQuestion string
	}{
		{"decision", "", "Which cache layout should we use?", core.KindDecision, "", ""},
		{"authorization", "", "Reply go to start.", core.KindGo, "", ""},
		{"chat", "", "Switch to this chat and enter the login code.", core.KindChore, "chat", ""},
		{"review", "#42", "#42 is green and ready for your review.", core.KindChore, "", "https://github.com/example/query/pull/42"},
		{"review", "example/query#7", "example/query#7 needs your eyes.", core.KindChore, "", "https://github.com/example/query/pull/7"},
		{"proposal", "https://example.com/design", "Please evaluate https://example.com/design", core.KindChore, "", "https://example.com/design"},
		{"proposal", "", "Should we adopt the new layout?", core.KindDecision, "", ""},
	} {
		t.Run(tc.kind+"/"+tc.target, func(t *testing.T) {
			c, dir := handoffWorld(t)
			m := underManager(t, c)
			var first string
			for _, id := range []string{"parent", "subagent-recap"} {
				target, err := applyHandoff(c, core.Board{}, handoffClaim(dir, id, tc.kind, tc.target, tc.text))
				if err != nil {
					t.Fatal(err)
				}
				if first == "" {
					first = target
				} else if target != first {
					t.Fatal("repeated request filed a second item")
				}
			}
			it, err := c.Store.Item(strings.TrimPrefix(first, "item:"))
			if err != nil {
				t.Fatal(err)
			}
			if it.Kind != tc.wantKind || it.Action != tc.wantAction || !strings.Contains(it.Question, tc.wantInQuestion) || it.Session != "lead" || it.State != core.ItemOpen || !strings.Contains(it.Assumption, "/tmp/agent.jsonl") || it.Recipient != m.ID {
				t.Fatalf("filed %+v", it)
			}
			msgs, _ := c.Store.Messages("boss")
			if len(msgs) != 1 || !strings.Contains(msgs[0].Text, tc.text) {
				t.Fatalf("manager got %+v, want one message carrying the agent's words %q", msgs, tc.text)
			}
		})
	}
}

func TestHandoffMatchingAnExistingItemFilesNothingNew(t *testing.T) {
	for _, tc := range []struct {
		name, session, kind string
		ok                  bool
	}{
		{"same session", "lead", "authorization", true},
		{"another session", "other", "authorization", false},
		{"review against a decision", "lead", "review", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := handoffWorld(t)
			it, err := c.Store.AddItem(core.Item{Scope: dir, Session: tc.session, Kind: core.KindGo, Question: "Start A+B?"})
			if err != nil {
				t.Fatal(err)
			}
			if it, err = c.Store.Transition(it.Key(), core.ItemAnswered, "Go A+B now"); err != nil {
				t.Fatal(err)
			}
			claim := handoffClaim(dir, "go", tc.kind, "", "Reply go A+B to start.")
			claim.Existing = "item:" + it.Key()
			target, err := applyHandoff(c, core.Board{}, claim)
			if (err == nil) != tc.ok || tc.ok && target != "item:"+it.Key() {
				t.Fatalf("target %q err %v", target, err)
			}
			items, _ := c.Store.Items(core.ItemFilter{})
			if len(items) != 1 || items[0].Answer != "Go A+B now" {
				t.Fatalf("owner answer duplicated or changed: %+v", items)
			}
		})
	}
}

func TestHandoffRejectsTargetsTheEventNeverNamed(t *testing.T) {
	c, dir := handoffWorld(t)
	if _, err := applyHandoff(c, core.Board{}, handoffClaim(dir, "r", "review", "#43", "#42 is ready for your review.")); err == nil {
		t.Fatal("ungrounded PR reference was filed")
	}
	if items, _ := c.Store.Items(core.ItemFilter{}); len(items) != 0 {
		t.Fatalf("filed %+v", items)
	}
}

func TestHandoffRecordsTheNamedHumanItActsFor(t *testing.T) {
	for _, requester := range []string{"Brian", ""} {
		c, dir := handoffWorld(t)
		underManager(t, c)
		claim := handoffClaim(dir, "choice", "decision", "", "Brian asked: which cache layout?")
		claim.Requester = requester
		target, err := applyHandoff(c, core.Board{}, claim)
		if err != nil {
			t.Fatal(err)
		}
		it, _ := c.Store.Item(strings.TrimPrefix(target, "item:"))
		if strings.Contains(it.Assumption, "on behalf of Brian") != (requester != "") {
			t.Fatalf("requester %q: %s", requester, it.Assumption)
		}
	}
}

func TestHandoffLedgerHoldsTheSessionsOpenAndAnsweredItemsAndPriorClaims(t *testing.T) {
	c, dir := handoffWorld(t)
	for _, f := range []struct{ session, state string }{{"lead", core.ItemOpen}, {"lead", core.ItemAnswered}, {"lead", core.ItemDismissed}, {"other", core.ItemOpen}} {
		it, err := c.Store.AddItem(core.Item{Scope: dir, Session: f.session, Kind: core.KindDecision, Question: f.session + " " + f.state})
		if err != nil {
			t.Fatal(err)
		}
		if f.state != core.ItemOpen {
			if _, err = c.Store.Transition(it.Key(), f.state, "done"); err != nil {
				t.Fatal(err)
			}
		}
	}
	claim := handoffClaim(dir, "prior", "decision", "", "Which layout?")
	if err := c.Store.Handoffs.Classified(nil, []handoff.Claim{claim}); err != nil {
		t.Fatal(err)
	}
	if err := c.Store.Handoffs.Applied(claim.ID, "item:#1"); err != nil {
		t.Fatal(err)
	}
	raw, err := handoffLedger(c, claim.Event)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(raw)
	got := string(encoded)
	for _, want := range []string{"lead open", "lead answered", "claim:prior", "example/query"} {
		if !strings.Contains(got, want) {
			t.Fatalf("ledger misses %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"lead dismissed", "other open"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("ledger holds %q: %s", unwanted, got)
		}
	}
}

// flood is a redacted real message: the agent's last turn refers to an ask block, and extraction lifted only its last line.
const flood = `The worker reports it is done and the validator is checking it.

Open for you
1. Keep the old client shim for one release?
2. Rotate the staging token now?
3. Ship the dashboard behind a flag?
4. Keep the retry wrapper or switch to the new client's built-in retries?

Next: answer item 4 with "keep" or "switch" while the validator runs.`

func TestHandoffsFromASessionTheOwnerStartedFileNoItemsAndKeepTheMessageWhole(t *testing.T) {
	c, dir := handoffWorld(t)
	kinds := []string{"decision", "authorization", "chat", "decision", "proposal", "decision", "chat", "authorization", "decision", "proposal"}
	for i, kind := range kinds {
		claim := handoffClaim(dir, fmt.Sprintf("ask-%d", i), kind, "", flood)
		claim.Question, claim.Topic = `Answer open item 4 with either "keep" or "switch".`, fmt.Sprintf("topic %d", i)
		target, err := applyHandoff(c, core.Board{}, claim)
		if err != nil || target != "waiting:lead" {
			t.Fatalf("ask %d became %q (%v), want the session's waiting row", i, target, err)
		}
	}
	if items, _ := c.Store.Items(core.ItemFilter{}); len(items) != 0 {
		t.Fatalf("an owner-started session's handoffs filed %d items", len(items))
	}
}
