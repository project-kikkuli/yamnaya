package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestAnAskBoundForTheOwnerMustBeReadyToDecide(t *testing.T) {
	for _, tc := range []struct {
		name    string
		from    string
		it      Item
		wantErr bool
	}{
		{name: "manager decision without rec", from: "manager", it: Item{Kind: KindDecision, Question: "q", Options: picks}, wantErr: true},
		{name: "manager decision with one option", from: "manager", it: Item{Kind: KindDecision, Question: "q", Rec: "a", Options: picks[:1]}, wantErr: true},
		{name: "manager decision option without effect", from: "manager", it: Item{Kind: KindDecision, Question: "q", Rec: "a", Options: []Option{{Label: "a"}, {Label: "b"}}}, wantErr: true},
		{name: "manager decision ready", from: "manager", it: Item{Kind: KindDecision, Question: "q", Rec: "a", Options: picks}},
		{name: "manager default without rec", from: "manager", it: Item{Kind: KindDefault, Question: "q"}, wantErr: true},
		{name: "manager chore needs no rec", from: "manager", it: Item{Kind: KindChore, Question: "review and merge #12"}},
		{name: "manager go needs no rec", from: "manager", it: Item{Kind: KindGo, Question: "build?"}},
		{name: "owner's own session decision without rec", from: "mine", it: Item{Kind: KindDecision, Question: "q", Options: picks}, wantErr: true},
		{name: "agent's thin ask goes to its manager", from: "agent", it: Item{Kind: KindDecision, Question: "q"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
			w.spawn("agent", m.Session)
			w.spawn("mine", "")
			from := map[string]string{"manager": m.Session, "agent": "agent", "mine": "mine"}[tc.from]
			tc.it.Session, tc.it.Harness = from, "claude"
			_, err := w.core.AddItem(tc.it)
			if errors.Is(err, ErrNotReady) != tc.wantErr || (err != nil && !errors.Is(err, ErrNotReady)) {
				t.Fatalf("err = %v, want not-ready %v", err, tc.wantErr)
			}
			all, _ := w.core.Store.Items(ItemFilter{})
			if (len(all) == 0) != tc.wantErr {
				t.Fatalf("stored %d items; a refused ask must leave none and a ready one must stay", len(all))
			}
		})
	}
}

func TestAnEscalationArrivesWithItsGoalPullRequestsAndActivity(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
	url := "https://github.com/acme/auth-plane/pull/9"
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) {
		s.PRs = []PR{{URL: url, Repo: "acme/auth-plane", Number: 9}}
		s.Result = "opened the rotation PR"
	})
	if err := w.core.Store.SavePRStatus(url, PRStatus{State: "OPEN", Title: "Rotate keys", Failed: 1, Passed: 3, Fetched: w.clock}); err != nil {
		t.Fatal(err)
	}
	w.spawn("other", "")
	w.set("other", func(s *Session) {
		s.PRs = []PR{{URL: "https://github.com/acme/elsewhere/pull/1"}}
	})
	it, err := w.core.AddItem(Item{Session: m.Session, Harness: "claude", Kind: KindDecision, Question: "rotate in prod tonight?", Rec: "yes",
		Options: picks, Tried: "staging rotation", Evidence: []string{"https://ci.example/run/42"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := w.core.Store.Item(it.Key())
	if err != nil {
		t.Fatal(err)
	}
	d := got.Dossier
	if d.Goal != g.ID || d.GoalAsk != g.Ask || !strings.Contains(d.From, m.ID) {
		t.Fatalf("dossier goal %q %q from %q; want the goal and its manager", d.Goal, d.GoalAsk, d.From)
	}
	if len(d.PRs) != 1 || d.PRs[0].URL != url || d.PRs[0].CI != "failing" || d.PRs[0].Title != "Rotate keys" {
		t.Fatalf("prs = %+v; want only the goal's PR with its failing CI", d.PRs)
	}
	if !slices.ContainsFunc(d.Activity, func(a ActivityNote) bool { return strings.Contains(a.What, "opened the rotation PR") }) {
		t.Fatalf("activity = %+v; want the agent's result", d.Activity)
	}
	if got.Tried != "staging rotation" || !slices.Equal(got.Evidence, []string{"https://ci.example/run/42"}) {
		t.Fatalf("tried %q evidence %q; want what the manager cited", got.Tried, got.Evidence)
	}
	if !d.Since.IsZero() || !slices.Contains(d.Changes, "opened "+url+" (ci failing)") {
		t.Fatalf("since %v changes %q; want the PR as new since the goal began", d.Since, d.Changes)
	}
}

func TestAsksReportOnlyWhatChangedSinceTheOwnerLastLooked(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
	first := "https://github.com/acme/auth-plane/pull/1"
	second := "https://github.com/acme/auth-plane/pull/2"
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) { s.PRs = []PR{{URL: first}} })
	if err := w.core.Store.SavePRStatus(first, PRStatus{State: "OPEN", Pending: 2, Fetched: w.clock}); err != nil {
		t.Fatal(err)
	}
	one, err := w.core.AddItem(Item{Session: m.Session, Kind: KindChore, Question: "rotate the HSM key by hand", Rec: "do it before noon"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.core.Answer(w.board(t), one.Key(), "done"); err != nil {
		t.Fatal(err)
	}

	w.set("agent", func(s *Session) { s.PRs = []PR{{URL: first}, {URL: second}} })
	for url, st := range map[string]PRStatus{first: {State: "MERGED", Passed: 2}, second: {State: "OPEN", Failed: 1}} {
		st.Fetched = w.clock
		if err := w.core.Store.SavePRStatus(url, st); err != nil {
			t.Fatal(err)
		}
	}
	two, err := w.core.AddItem(Item{Session: m.Session, Kind: KindChore, Question: "approve the prod window", Rec: "approve"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := w.core.Store.Item(two.Key())
	want := []string{"merged " + first, "opened " + second + " (ci failing)"}
	if got.Dossier.Since.IsZero() || !slices.Equal(got.Dossier.Changes, want) {
		t.Fatalf("since %v changes %q; want %q", got.Dossier.Since, got.Dossier.Changes, want)
	}

	if err := w.core.Store.SavePRStatus(second, PRStatus{State: "OPEN", Passed: 3, Fetched: w.clock}); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Brief(w.board(t)); err != nil {
		t.Fatal(err)
	}
	got, _ = w.core.Store.Item(two.Key())
	if !slices.Contains(got.Dossier.Changes, "opened "+second+" (ci passing)") ||
		!slices.ContainsFunc(got.Dossier.PRs, func(p PRNote) bool { return p.URL == second && p.CI == "passing" }) {
		t.Fatalf("after ym serve refreshed, dossier = %+v; want the PR's CI now passing", got.Dossier)
	}
}

func TestAnAskThatClimbsPastADeadManagerReachesTheOwnerMarkedUnpreparedUntilTheManagerPreparesIt(t *testing.T) {
	for _, tc := range []struct {
		name        string
		it          Item
		wantMissing []string
	}{
		{name: "thin decision", it: Item{Kind: KindDecision, Question: "v2 key ids?"}, wantMissing: []string{"a recommendation", "two options with their effects"}},
		{name: "chore without rec", it: Item{Kind: KindChore, Question: "approve CHG-77"}, wantMissing: []string{"a recommendation"}},
		{name: "ready decision", it: Item{Kind: KindDecision, Question: "v2 key ids?", Rec: "v2", Options: picks}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
			w.spawn("agent", m.Session)
			tc.it.Session, tc.it.Harness = "agent", "claude"
			it, err := w.core.AddItem(tc.it)
			if err != nil || it.Recipient != m.ID {
				t.Fatalf("filed to %q, %v; want the manager", it.Recipient, err)
			}
			m, _ = w.core.Store.Manager(m.ID)
			m.Failures = stuckFailures
			if err := w.core.Store.SaveManager(m); err != nil {
				t.Fatal(err)
			}
			w.stop(m.Session)
			before := len(w.handedTo(m.Session))
			w.supervise(t)
			got, _ := w.core.Store.Item(it.Key())
			if got.Recipient != Owner || !slices.Equal(Missing(got), tc.wantMissing) {
				t.Fatalf("recipient %q missing %q; want the owner and %q", got.Recipient, Missing(got), tc.wantMissing)
			}
			if got.Dossier.Goal == "" {
				t.Fatalf("the climbed ask carries no evidence: %+v", got.Dossier)
			}
			pending, _ := w.core.Store.PendingMessages()
			told := slices.ContainsFunc(append(w.handedTo(m.Session)[before:], texts(pending)...), func(s string) bool {
				return strings.Contains(s, "ym item prepare '"+it.Key()+"'")
			})
			if told != (tc.wantMissing != nil) || (got.Preparer == m.ID) != told {
				t.Fatalf("manager told to prepare %v, preparer %q; want told %v", told, got.Preparer, tc.wantMissing != nil)
			}
			if tc.wantMissing == nil {
				return
			}
			if _, err := w.core.Prepare(w.board(t), it.Key(), Prepared{Rec: "v2"}); tc.it.Kind == KindDecision && !errors.Is(err, ErrNotReady) {
				t.Fatalf("a decision prepared without options: err %v, want it refused", err)
			}
			prepared, err := w.core.Prepare(w.board(t), it.Key(), Prepared{Rec: "v2", Options: picks, Evidence: []string{"https://ci.example/run/7"}})
			if err != nil || len(Missing(prepared)) != 0 || prepared.Preparer != "" || !slices.Contains(prepared.Evidence, "https://ci.example/run/7") {
				t.Fatalf("prepared = %+v, %v; want it ready with its evidence and no preparer left", prepared, err)
			}
		})
	}
}

func texts(ms []Message) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Text)
	}
	return out
}
