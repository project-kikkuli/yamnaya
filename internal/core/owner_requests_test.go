package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestOwnerRequestsSurviveManagerAndChiefSessionChanges(t *testing.T) {
	for _, role := range []string{RoleManager, RoleChief} {
		for _, state := range []string{"refreshed", "down", "retired"} {
			t.Run(role+"/"+state, func(t *testing.T) {
				w := newOrgWorld(t)
				var m Manager
				if role == RoleChief {
					m = w.chief(t, "platform")
				} else {
					_, m = w.goal(t, "land the platform", "/repo/platform")
				}
				it := w.ask(t, m.Session, "approve the landing plan?")
				original := m.Session
				w.stop(original)
				if state == "refreshed" {
					m.Session = "replacement"
					w.spawn(m.Session, "")
				}
				m.Retired = state == "retired"
				if err := w.core.Store.SaveManager(m); err != nil {
					t.Fatal(err)
				}
				b := w.board(t)
				if !slices.ContainsFunc(b.Needs, func(n Need) bool { return n.Item != nil && n.Item.Seq == it.Seq && n.Manager == m.ID }) || len(b.Stale) != 0 {
					t.Fatalf("owner request disappeared: needs=%+v stale=%+v", b.Needs, b.Stale)
				}
				if state == "refreshed" {
					if _, _, err := w.core.Answer(b, it.Key(), "approved"); err != nil {
						t.Fatal(err)
					}
					if _, err := w.core.Redeliver(w.board(t)); err != nil {
						t.Fatal(err)
					}
					if !slices.ContainsFunc(w.handedTo(m.Session), func(s string) bool { return strings.Contains(s, "approved") }) {
						t.Fatalf("answer did not reach current manager: %v", w.claude.handed)
					}
				}
			})
		}
	}
}

func TestEscalatingAnExistingItemPreservesItsIdentityAndAnswersItsSource(t *testing.T) {
	w := newOrgWorld(t)
	_, manager := w.goal(t, "land the platform", "/repo/platform")
	w.spawn("writer", manager.Session)
	it := w.ask(t, "writer", "approve the direction?")
	if it.Recipient != manager.ID {
		t.Fatalf("ask is not delegated: %+v", it)
	}
	for range 2 {
		got, err := w.core.Escalate(it.Key())
		if err != nil || got.Recipient != Owner || got.Seq != it.Seq || got.ID != it.ID || got.Session != it.Session || !got.CreatedAt.Equal(it.CreatedAt) || got.Dossier.Goal != manager.GoalID {
			t.Fatalf("escalation changed identity: %+v, %v", got, err)
		}
	}
	all, err := w.core.Store.Items(ItemFilter{})
	if err != nil || len(all) != 1 {
		t.Fatalf("escalation duplicated the ask: %+v, %v", all, err)
	}
	if got, _ := w.core.Store.Item(it.Key()); len(got.History) != 2 || !strings.Contains(got.History[1].Text, "escalated to owner") {
		t.Fatalf("escalation history: %+v", got.History)
	}
	if _, _, err := w.core.Answer(w.board(t), it.Key(), "approved"); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(w.handedTo("writer"), func(s string) bool { return strings.Contains(s, "approved") }) {
		t.Fatalf("answer did not reach original writer: %v", w.claude.handed)
	}
}

func TestEscalationRequiresAnOpenPreparedRequest(t *testing.T) {
	for _, state := range []string{"thin", ItemAnswered, ItemDismissed, ItemSnoozed} {
		t.Run(state, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "land", "/repo/platform")
			w.spawn("writer", m.Session)
			it, err := w.core.AddItem(Item{Kind: KindDecision, Session: "writer", Question: "approve?"})
			if err != nil {
				t.Fatal(err)
			}
			if state != "thin" {
				if _, err := w.core.Store.Transition(it.Key(), state, "settled"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.core.Escalate(it.Key()); err == nil || state == "thin" && !errors.Is(err, ErrNotReady) {
				t.Fatalf("unexpected escalation result: %v", err)
			}
			if got, _ := w.core.Store.Item(it.Key()); got.Recipient != m.ID {
				t.Fatalf("refused escalation changed recipient: %+v", got)
			}
		})
	}
}

func TestExplicitReviewsReachTheOwnerAndSurviveTheirWriterExiting(t *testing.T) {
	for _, document := range []string{"https://claude.ai/artifact/plan", "/tmp/landing-plan.md"} {
		t.Run(document, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "land", "/repo/platform")
			w.spawn("writer", m.Session)
			it, err := w.core.AddItem(Item{Kind: KindReview, Session: "writer", Question: "review the landing plan", Rec: "approve", Evidence: []string{document}})
			if err != nil || it.Recipient != Owner || !strings.HasPrefix(it.ID, "R") {
				t.Fatalf("review did not reach owner: %+v, %v", it, err)
			}
			w.stop("writer")
			w.stop(m.Session)
			b := w.board(t)
			if !slices.ContainsFunc(ownerItems(b), func(i Item) bool { return i.Seq == it.Seq && slices.Equal(i.Evidence, []string{document}) }) {
				t.Fatalf("review disappeared after writer exited: %+v", b.Needs)
			}
		})
	}
}

func TestReviewRegistrationRequiresDocumentAndRecommendation(t *testing.T) {
	for _, it := range []Item{
		{Rec: "approve"},
		{Rec: "approve", Evidence: []string{" "}},
		{Rec: "approve", Evidence: []string{" ", "https://ci.example/run"}},
		{Evidence: []string{"https://claude.ai/artifact/plan"}},
	} {
		w := newOrgWorld(t)
		it.Kind, it.Session, it.Question = KindReview, "writer", "review the plan"
		if _, err := w.core.AddItem(it); !errors.Is(err, ErrNotReady) {
			t.Fatalf("incomplete review accepted: %+v, %v", it, err)
		}
	}
}

func TestAnExplicitOwnerDestinationBypassesTheChief(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "land", "/repo/platform")
	w.chief(t, "platform", m.ID)
	it, err := w.core.AddOwnerItem(Item{Kind: KindDecision, Session: m.Session, Question: "choose direction", Rec: "a", Options: picks})
	if err != nil || it.Recipient != Owner || len(ownerItems(w.board(t))) != 1 {
		t.Fatalf("explicit owner request was delegated: %+v, %v", it, err)
	}
}

func TestExitedManagedSessionsKeepTheirOwnership(t *testing.T) {
	for _, role := range []string{RoleChief, RoleManager} {
		t.Run(role, func(t *testing.T) {
			w := newOrgWorld(t)
			var m Manager
			if role == RoleChief {
				m = w.chief(t, "platform")
			} else {
				_, m = w.goal(t, "land", "/repo/platform")
			}
			old := m.Session
			w.stop(old)
			m.Session = "replacement"
			if err := w.core.Store.SaveManager(m); err != nil {
				t.Fatal(err)
			}
			w.core.Launches = &dormantLaunches{dormant: []Dormant{
				{Key: "previous", Session: old, Harness: "claude"},
				{Key: "child", Session: "writer", Parent: old, Harness: "claude"},
			}}
			b := w.board(t)
			for _, p := range b.Projects {
				if len(p.Dormant) > 0 && p.Manager != m.ID {
					t.Fatalf("exited managed session became owner-started: %+v", p)
				}
			}
		})
	}
}
