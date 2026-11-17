package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestNativeActionsWaitForMachineCapacityWithoutConsumingIdentityOrHumanAttention(t *testing.T) {
	for _, reason := range []string{"memory pressure", "processor saturated", "filesystem full", "capacity telemetry unavailable"} {
		t.Run(reason, func(t *testing.T) {
			w := newOrgWorld(t)
			_, manager := w.goal(t, "repair importer", "/repo/august")
			w.set(manager.Session, func(session *Session) { session.Cwd = "/runtime/worktree" })
			for n := range 3 {
				if err := w.core.requestAction(w.board(t), manager.Session, "directive", fmt.Sprint(n), fmt.Sprintf("repair parser %d", n)); err != nil {
					t.Fatal(err)
				}
			}
			before, err := w.core.Store.Activations()
			if err != nil {
				t.Fatal(err)
			}
			identities := map[string]bool{}
			for _, action := range before {
				identities[action.ID] = true
			}
			pressured := true
			checks := 0
			var submitted []string
			w.core.LaunchAdmission = func(request NewLaunch) error {
				checks++
				live, ok := w.board(t).Session(manager.Session)
				if !ok {
					t.Fatal("native session missing")
				}
				if request.Repo != live.Cwd || request.Harness != live.Harness || request.Profile != live.Profile {
					t.Fatalf("admission probed another runtime: %+v %+v", request, live.Session)
				}
				if pressured {
					return &LaunchDeferred{Reason: reason}
				}
				return nil
			}
			w.core.BackgroundSend = func(_ Live, text string) error { submitted = append(submitted, text); return nil }
			if err := w.core.DispatchActions(w.board(t)); err != nil {
				t.Fatal(err)
			}
			var changes int64
			if err := w.core.Store.db.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if err := w.core.DispatchActions(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			var afterChanges int64
			if err := w.core.Store.db.QueryRow(`SELECT total_changes()`).Scan(&afterChanges); err != nil {
				t.Fatal(err)
			}
			if afterChanges != changes {
				t.Fatalf("unchanged pressure caused repeated database writes: %d -> %d", changes, afterChanges)
			}
			if len(submitted) != 0 || len(w.handedTo(manager.Session)) != 0 {
				t.Fatal("capacity check activated native work")
			}
			actions, err := w.core.Store.Activations()
			if err != nil || len(actions) != 3 {
				t.Fatalf("pending work lost: %+v %v", actions, err)
			}
			for _, action := range actions {
				if !identities[action.ID] || action.State != "pending" || action.Attempt != 0 || action.Attention != 0 || action.Batch != "" || action.TargetSession != "" || !action.Dispatched.IsZero() || !strings.Contains(action.Error, reason) {
					t.Fatalf("capacity deferral changed submission identity: %+v", action)
				}
			}
			if items, err := w.core.Store.Items(ItemFilter{}); err != nil || len(items) != 0 {
				t.Fatalf("capacity checks flooded human inbox: %+v %v", items, err)
			}
			if checks != 4 {
				t.Fatalf("capacity checks %d, want once per native batch", checks)
			}
			pressured = false
			for range 3 {
				if err := w.core.DispatchActions(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			if len(submitted) != 1 || strings.Count(submitted[0], "Yamnaya supervisor action ") != 3 {
				t.Fatalf("release replayed or split batch: %q", submitted)
			}
			actions, err = w.core.Store.Activations()
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range actions {
				if !identities[action.ID] || action.Attempt != 1 || action.State != "sent" || action.Error != "" || action.Attention != 0 {
					t.Fatalf("release changed identity or retained capacity error: %+v", action)
				}
			}
		})
	}
}

func TestObsoleteQuestionCancelsWhileMachineCapacityIsDeferred(t *testing.T) {
	w := newOrgWorld(t)
	_, manager := w.goal(t, "repair importer", "/repo/august")
	w.spawn("worker", manager.Session)
	item := w.ask(t, "worker", "Which parser should I use?")
	checks := 0
	w.core.LaunchAdmission = func(NewLaunch) error { checks++; return &LaunchDeferred{Reason: "memory pressure"} }
	submitted := 0
	w.core.BackgroundSend = func(Live, string) error { submitted++; return nil }
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.Transition(item.Key(), ItemAnswered, "streaming parser"); err != nil {
		t.Fatal(err)
	}
	if err := w.core.DispatchActions(w.board(t)); err != nil {
		t.Fatal(err)
	}
	actions, err := w.core.Store.Activations()
	if err != nil || len(actions) != 1 || actions[0].State != "superseded" || actions[0].Attempt != 0 || actions[0].Attention != 0 || actions[0].Error != "" {
		t.Fatalf("pressure hid current source facts: %+v %v", actions, err)
	}
	if submitted != 0 || checks != 1 {
		t.Fatalf("obsolete action reached native admission: checks=%d turns=%d", checks, submitted)
	}
}

func TestRemoteActionsUseDestinationCapacityAndCancellationBypassesNativeAdmission(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "destination release", true: "source cancellation"}[cancel], func(t *testing.T) {
			f := newFleet(t)
			local := f.b.chief(t, "local")
			remote := f.c.chief(t, "remote")
			if err := f.b.core.RequestDirective(local.Session, "Repair local work"); err != nil {
				t.Fatal(err)
			}
			if err := f.b.core.RequestDirective(RemoteKey("c", remote.ID), "Repair remote work"); err != nil {
				t.Fatal(err)
			}
			sourceChecks, sourceTurns, destinationChecks, destinationTurns := 0, 0, 0, 0
			f.b.core.LaunchAdmission = func(NewLaunch) error { sourceChecks++; return &LaunchDeferred{Reason: "source memory pressure"} }
			f.b.core.BackgroundSend = func(Live, string) error { sourceTurns++; return nil }
			destinationPressure := true
			f.c.core.LaunchAdmission = func(NewLaunch) error {
				destinationChecks++
				if destinationPressure {
					return &LaunchDeferred{Reason: "destination processor pressure"}
				}
				return nil
			}
			f.c.core.BackgroundSend = func(Live, string) error { destinationTurns++; return nil }
			if err := f.b.core.DispatchActions(f.b.board(t)); err != nil {
				t.Fatal(err)
			}
			actions, err := f.b.core.Store.Activations()
			if err != nil {
				t.Fatal(err)
			}
			var remoteAction Activation
			for _, action := range actions {
				if action.Session == RemoteKey("c", remote.ID) {
					remoteAction = action
				}
			}
			if remoteAction.ID == "" || remoteAction.State != "transferred" || sourceChecks != 1 || sourceTurns != 0 {
				t.Fatalf("local pressure blocked remote transfer: %+v checks=%d turns=%d", actions, sourceChecks, sourceTurns)
			}
			pending, err := f.b.core.Store.PendingRelays()
			if err != nil {
				t.Fatal(err)
			}
			offered := 0
			for _, relay := range pending {
				if relay.Kind == RelayAction {
					var message ActivationMessage
					if err := json.Unmarshal([]byte(relay.Body), &message); err != nil {
						t.Fatal(err)
					}
					if message.Operation == "activate" && message.Action.ID == remoteAction.ID && relay.Dest == "c" {
						offered++
					}
				}
			}
			if offered != 1 {
				t.Fatalf("remote transfer identity missing: %d", offered)
			}
			for range 3 {
				f.sync(t)
			}
			if err := f.c.core.DispatchActions(f.c.board(t)); err != nil {
				t.Fatal(err)
			}
			targetActions, err := f.c.core.Store.Activations()
			if err != nil || len(targetActions) != 1 || targetActions[0].ID != remoteAction.ID || targetActions[0].State != "pending" || targetActions[0].Attempt != 0 || !strings.Contains(targetActions[0].Error, "destination processor pressure") {
				t.Fatalf("destination did not apply its own capacity: %+v %v", targetActions, err)
			}
			if cancel {
				if err := f.b.core.finishActivation(remoteAction, "superseded", "work is no longer needed"); err != nil {
					t.Fatal(err)
				}
				for range 3 {
					f.sync(t)
				}
				if err := f.c.core.DispatchActions(f.c.board(t)); err != nil {
					t.Fatal(err)
				}
				targetActions, err = f.c.core.Store.Activations()
				if err != nil || targetActions[0].State != "superseded" || targetActions[0].Attempt != 0 {
					t.Fatalf("pressure blocked source cancellation: %+v %v", targetActions, err)
				}
				if destinationChecks != 1 || destinationTurns != 0 {
					t.Fatalf("cancellation required a native turn: checks=%d turns=%d", destinationChecks, destinationTurns)
				}
			} else {
				destinationPressure = false
				for range 2 {
					if err := f.c.core.DispatchActions(f.c.board(t)); err != nil {
						t.Fatal(err)
					}
				}
				if destinationTurns != 1 {
					t.Fatalf("destination release sent %d turns, want once", destinationTurns)
				}
				for range 3 {
					f.sync(t)
				}
				source, err := f.b.core.Store.Activations()
				if err != nil {
					t.Fatal(err)
				}
				for _, action := range source {
					if action.ID == remoteAction.ID && action.State != "sent" {
						t.Fatalf("pressure blocked acceptance receipt: %+v", action)
					}
				}
			}
			if sourceChecks != 1 || sourceTurns != 0 {
				t.Fatalf("destination handling re-admitted source work: checks=%d turns=%d", sourceChecks, sourceTurns)
			}
			for _, world := range []*orgWorld{f.b, f.c} {
				if items, err := world.core.Store.Items(ItemFilter{}); err != nil || len(items) != 0 {
					t.Fatalf("capacity created recovery inbox items: %+v %v", items, err)
				}
			}
		})
	}
}
