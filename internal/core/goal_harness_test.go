package core

import (
	"strings"
	"testing"
	"time"
)

func TestAGoalRunsOnTheHarnessItIsStartedOnThroughVerification(t *testing.T) {
	for _, tc := range []struct {
		harness, runs, signs, newAgent string
	}{
		{"", "claude", `"Claude: ..."`, `ym new --repo /repo/august "<prompt>" (add --codex`},
		{"claude", "claude", `"Claude: ..."`, `ym new --repo /repo/august "<prompt>" (add --codex`},
		{"codex", "codex", `"Codex: ..."`, `ym new --codex --repo /repo/august "<prompt>"`},
	} {
		t.Run(tc.runs+"/"+tc.harness, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Landings = fakeLandings{}
			g, m, err := w.core.NewGoal("make the importer handle CSV", "/repo/august", tc.harness)
			if err != nil {
				t.Fatal(err)
			}
			start := w.launches.starts[0]
			if m.Harness != tc.runs || start.Harness != tc.runs || !start.Await {
				t.Fatalf("manager on %q started %+v, want %s awaiting its id", m.Harness, start, tc.runs)
			}
			if w.launches.harnessOf[m.Session] != tc.runs {
				t.Fatalf("manager keyed by %s, not the session its launch reported", m.Session)
			}
			for _, want := range []string{tc.signs, tc.newAgent} {
				if !strings.Contains(start.Prompt, want) {
					t.Fatalf("manager prompt lacks %q:\n%s", want, start.Prompt)
				}
			}
			w.set(m.Session, func(s *Session) { s.Status = Busy })
			if _, err := w.core.UpdateGoal(m.Session, "parser done"); err != nil {
				t.Fatalf("the manager's own session is not taken as the manager: %v", err)
			}
			if _, _, err := w.core.FinishGoal(m.Session, "importer reads CSV", nil); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Second)
			if err := w.core.Loop(w.board(t)); err != nil {
				t.Fatal(err)
			}
			verifier := w.launches.starts[len(w.launches.starts)-1]
			if !strings.HasPrefix(verifier.Slug, "verify-") || verifier.Harness != tc.runs || !verifier.Await {
				t.Fatalf("verifier started %+v, want one on %s awaiting its id", verifier, tc.runs)
			}
			all, err := w.core.Store.Verifications(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.RecordVerdict(all[len(all)-1].Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatalf("the verifier's own session could not record its verdict: %v", err)
			}
			if now, _ := w.core.Store.Goal(g.ID); now.State != GoalDone {
				t.Fatalf("goal %s after a pass, want done", now.State)
			}
		})
	}
}

func TestManagersAndChiefsRunOnlyOnClaudeOrCodex(t *testing.T) {
	w := newOrgWorld(t)
	if _, _, err := w.core.NewGoal("make the importer handle CSV", "/repo/august", "gemini"); err == nil || !strings.Contains(err.Error(), "claude or codex") {
		t.Fatalf("a goal on gemini: %v", err)
	}
	c, err := w.core.NewChief("platform", "", "/repo/august", "codex", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Harness != "codex" || w.launches.starts[0].Harness != "codex" || w.launches.harnessOf[c.Session] != "codex" {
		t.Fatalf("chief %+v started %+v, want both on codex", c, w.launches.starts[0])
	}
}
