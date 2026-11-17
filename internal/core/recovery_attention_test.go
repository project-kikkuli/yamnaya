package core

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func assertRecoveryPollIsReadOnly(t *testing.T, store *Store, poll func() error) {
	t.Helper()
	var before, after int64
	if err := store.db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	store.memo.mu.Lock()
	if store.memo.vals == nil {
		store.memo.vals = map[string]any{}
	}
	store.memo.vals["recovery-poll-cache"] = true
	store.memo.mu.Unlock()
	for range 3 {
		if err := poll(); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.db.QueryRow(`SELECT total_changes()`).Scan(&after); err != nil || after != before {
		t.Fatalf("passive recovery poll wrote rows: %d -> %d, %v", before, after, err)
	}
	store.memo.mu.Lock()
	cached := store.memo.vals["recovery-poll-cache"]
	delete(store.memo.vals, "recovery-poll-cache")
	store.memo.mu.Unlock()
	if cached != true {
		t.Fatal("passive recovery poll invalidated cached board reads")
	}
}

func TestOwnerRecoveryDispositionSurvivesPollingAndRetryReusesBatchNotice(t *testing.T) {
	for _, state := range []string{"blocked", "uncertain"} {
		for _, disposition := range []string{ItemDismissed, ItemAnswered} {
			t.Run(state+"/"+disposition, func(t *testing.T) {
				w := newOrgWorld(t)
				_, m := w.goal(t, "repair importer", "/repo/august")
				calls := 0
				failure := errors.New("native transport unavailable")
				if state == "uncertain" {
					failure = fmt.Errorf("missing native receipt: %w", ErrSubmissionUncertain)
				}
				w.core.BackgroundSend = func(Live, string) error { calls++; return failure }
				for _, subject := range []string{"one", "two", "three"} {
					if err := w.core.requestAction(w.board(t), m.Session, "directive", subject, "repair "+subject); err != nil {
						t.Fatal(err)
					}
				}
				dispatch := func() {
					t.Helper()
					if err := w.core.DispatchActions(w.board(t)); err != nil {
						t.Fatal(err)
					}
				}
				dispatch()
				actions, err := w.core.Store.Activations()
				if err != nil || len(actions) != 3 {
					t.Fatalf("actions %+v, %v", actions, err)
				}
				notice := actions[0].Attention
				for _, a := range actions {
					if a.State != state || a.Attention != notice || notice == 0 || a.AttentionAttempt != a.Attempt || a.Attempt != 1 {
						t.Fatalf("batch recovery not coalesced: %+v", actions)
					}
				}
				item, err := w.core.Store.Transition(fmt.Sprintf("#%d", notice), disposition, "owner inspected the failure")
				if err != nil {
					t.Fatal(err)
				}
				for range 3 {
					dispatch()
					for _, a := range actions {
						if err := w.core.actionAttention(a, "different passive observation"); err != nil {
							t.Fatal(err)
						}
					}
				}
				current, err := w.core.Store.Activations()
				if err != nil {
					t.Fatal(err)
				}
				assertRecoveryPollIsReadOnly(t, w.core.Store, func() error {
					for _, a := range current {
						if err := w.core.actionAttention(a, a.Error); err != nil {
							return err
						}
					}
					return nil
				})
				items, err := w.core.Store.Items(ItemFilter{})
				if err != nil || len(items) != 1 || items[0].Seq != notice || items[0].State != disposition || len(items[0].History) != len(item.History) || calls != 1 {
					t.Fatalf("owner disposition lost: %+v, calls %d, %v", items, calls, err)
				}
				if err := w.core.RetryAction(actions[0].ID); err != nil {
					t.Fatal(err)
				}
				dispatch()
				items, err = w.core.Store.Items(ItemFilter{})
				if err != nil || len(items) != 1 || items[0].Seq != notice || items[0].State != ItemOpen || len(items[0].History) != len(item.History)+1 || calls != 2 {
					t.Fatalf("retry replaced notice/history: %+v, calls %d, %v", items, calls, err)
				}
				retried, err := w.core.Store.Activations()
				if err != nil {
					t.Fatal(err)
				}
				for _, a := range retried {
					if a.Attention != notice || a.AttentionAttempt != 2 || a.Attempt != 2 {
						t.Fatalf("retry lost batch association: %+v", retried)
					}
				}
			})
		}
	}
}

func TestAttentionMigrationBindsExistingAttemptsOnlyOnce(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ym_activations (attention INTEGER NOT NULL, attempt INTEGER NOT NULL); INSERT INTO ym_activations VALUES(42,3)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateActivationAttention(db); err != nil {
		t.Fatal(err)
	}
	var associated int
	if err := db.QueryRow(`SELECT attention_attempt FROM ym_activations`).Scan(&associated); err != nil || associated != 3 {
		t.Fatalf("legacy notice not bound: %d, %v", associated, err)
	}
	if _, err := db.Exec(`UPDATE ym_activations SET attempt=4`); err != nil {
		t.Fatal(err)
	}
	if err := migrateActivationAttention(db); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT attention_attempt FROM ym_activations`).Scan(&associated); err != nil || associated != 3 {
		t.Fatalf("existing association overwritten on reopen: %d, %v", associated, err)
	}
}

func TestLaunchRetryRetainsNoticeAndCapacityDeferralDoesNotReopenIt(t *testing.T) {
	for _, disposition := range []string{ItemDismissed, ItemAnswered} {
		for _, deferRetry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deferred=%v", disposition, deferRetry), func(t *testing.T) {
				w := newOrgWorld(t)
				w.launches.fail = errors.New("native startup refused")
				n := NewLaunch{Repo: "/repo/app", Session: "preserved-worker", Source: "one-source", Prompt: "one action"}
				if _, err := w.core.StartNew(n); err == nil {
					t.Fatal("startup failure hidden")
				}
				pending, err := w.core.Store.PendingLaunches()
				if err != nil || len(pending) != 1 || pending[0].Attention == 0 || pending[0].Attempt != 1 || pending[0].AttentionAttempt != 1 {
					t.Fatalf("missing typed launch recovery: %+v, %v", pending, err)
				}
				launch := pending[0]
				item, err := w.core.Store.Transition(fmt.Sprintf("#%d", launch.Attention), disposition, "owner inspected native effects")
				if err != nil {
					t.Fatal(err)
				}
				for range 3 {
					if err := w.core.DrainLaunches(); err != nil {
						t.Fatal(err)
					}
					if err := w.core.launchAttention(launch.ID, launch.Launch, "same attempt remains blocked"); err != nil {
						t.Fatal(err)
					}
				}
				assertRecoveryPollIsReadOnly(t, w.core.Store, func() error {
					return w.core.launchAttention(launch.ID, launch.Launch, "same attempt remains blocked")
				})
				items, err := w.core.Store.Items(ItemFilter{})
				if err != nil || len(items) != 1 || items[0].Seq != item.Seq || items[0].State != disposition || len(items[0].History) != len(item.History) {
					t.Fatalf("passive launch checks lost owner disposition: %+v, %v", items, err)
				}
				if deferRetry {
					w.core.LaunchAdmission = func(NewLaunch) error { return &LaunchDeferred{Reason: "memory pressure"} }
					if _, err := w.core.RetryLaunch(launch.ID); err != nil {
						t.Fatal(err)
					}
					for range 3 {
						if err := w.core.DrainLaunches(); err != nil {
							t.Fatal(err)
						}
					}
					items, err = w.core.Store.Items(ItemFilter{})
					if err != nil || len(items) != 1 || items[0].State != disposition || len(items[0].History) != len(item.History) {
						t.Fatalf("capacity deferral reopened notice: %+v, %v", items, err)
					}
					w.core.LaunchAdmission = nil
					if err := w.core.DrainLaunches(); err == nil {
						t.Fatal("retried startup failure hidden")
					}
				} else if _, err := w.core.RetryLaunch(launch.ID); err == nil {
					t.Fatal("retried startup failure hidden")
				}
				items, err = w.core.Store.Items(ItemFilter{})
				if err != nil || len(items) != 1 || items[0].Seq != item.Seq || items[0].State != ItemOpen || len(items[0].History) != len(item.History)+1 {
					t.Fatalf("new launch failure lost notice identity/history: %+v, %v", items, err)
				}
				pending, err = w.core.Store.PendingLaunches()
				if err != nil || len(pending) != 1 || pending[0].Attention != item.Seq || pending[0].Attempt != 2 || pending[0].AttentionAttempt != 2 {
					t.Fatalf("new launch failure lost typed association: %+v, %v", pending, err)
				}
			})
		}
	}
}

func TestLauncherCapacityRecheckDoesNotConsumeRecoveryAttempt(t *testing.T) {
	w := newOrgWorld(t)
	w.launches.fail = errors.New("native startup refused")
	if _, err := w.core.StartNew(NewLaunch{Repo: "/repo/app", Session: "preserved-worker", Source: "one-source", Prompt: "one action"}); err == nil {
		t.Fatal("startup failure hidden")
	}
	pending, err := w.core.Store.PendingLaunches()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	launch := pending[0]
	item, err := w.core.Store.Transition(fmt.Sprintf("#%d", launch.Attention), ItemDismissed, "owner inspected native effects")
	if err != nil {
		t.Fatal(err)
	}
	admissions := 0
	w.core.LaunchAdmission = func(NewLaunch) error { admissions++; return nil }
	w.launches.fail = &LaunchDeferred{Reason: "pressure rose after admission"}
	if _, err := w.core.RetryLaunch(launch.ID); err != nil {
		t.Fatal(err)
	}
	pending, err = w.core.Store.PendingLaunches()
	if err != nil || admissions == 0 || len(pending) != 1 || pending[0].Attempt != 1 || pending[0].AttentionAttempt != 1 || pending[0].State != "pending" {
		t.Fatalf("launcher deferral consumed attempt: %+v, admissions %d, %v", pending, admissions, err)
	}
	it, err := w.core.Store.Item(item.Key())
	if err != nil || it.State != ItemDismissed || len(it.History) != len(item.History) {
		t.Fatalf("launcher deferral reopened notice: %+v, %v", it, err)
	}
	w.launches.fail = errors.New("native startup still refused")
	if err := w.core.DrainLaunches(); err == nil {
		t.Fatal("native startup failure hidden")
	}
	pending, err = w.core.Store.PendingLaunches()
	if err != nil || len(pending) != 1 || pending[0].Attempt != 2 || pending[0].Attention != item.Seq || pending[0].AttentionAttempt != 2 {
		t.Fatalf("later native failure lost attempt/notice identity: %+v, %v", pending, err)
	}
	it, err = w.core.Store.Item(item.Key())
	if err != nil || it.State != ItemOpen || len(it.History) != len(item.History)+1 {
		t.Fatalf("later native failure lost history: %+v, %v", it, err)
	}
}
