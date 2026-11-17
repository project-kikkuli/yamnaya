package core

import (
	"encoding/json"
	"testing"
)

func TestSupervisorActionTransferKeepsIdentityAcrossLostDeliveryAndCompletion(t *testing.T) {
	f := newFleet(t)
	chief := f.b.chief(t, "operations")
	target := RemoteKey("b", chief.ID)
	if err := f.a.core.RequestDirective(target, "Review the completed work"); err != nil {
		t.Fatal(err)
	}
	actions, err := f.a.core.Store.Activations()
	if err != nil || len(actions) != 1 {
		t.Fatalf("source actions: %v %v", actions, err)
	}
	original := actions[0]
	if handled, err := f.a.core.transferActivation(original, target); err != nil || !handled {
		t.Fatalf("transfer: %v %v", handled, err)
	}
	if handled, err := f.a.core.transferActivation(original, target); err != nil || !handled {
		t.Fatalf("repeat transfer: %v %v", handled, err)
	}
	relays, err := f.a.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	var message ActivationMessage
	count := 0
	for _, relay := range relays {
		if relay.Kind == RelayAction {
			count++
			if err := json.Unmarshal([]byte(relay.Body), &message); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count != 1 {
		t.Fatalf("duplicate transfer outbox: %d", count)
	}
	f.net.loseReplies = 1
	f.sync(t)
	f.sync(t)
	imported, err := f.b.core.Store.Activations()
	if err != nil || len(imported) != 1 {
		t.Fatalf("destination actions: %v %v", imported, err)
	}
	if imported[0].ID != original.ID || imported[0].Session != chief.Session || imported[0].Recipient != chief.ID {
		t.Fatalf("action binding changed: %+v", imported[0])
	}
	if err := f.b.core.ReceiveActivation(message); err != nil {
		t.Fatal(err)
	}
	if _, err := f.b.core.Store.db.Exec(`UPDATE ym_activations SET state='claimed',target_session=? WHERE id=?`, chief.Session, original.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.b.core.AcceptAction(original.ID, chief.Session); err != nil {
		t.Fatal(err)
	}
	if err := f.b.core.CompleteAction(original.ID, chief.Session, "landed"); err != nil {
		t.Fatal(err)
	}
	f.sync(t)
	f.sync(t)
	source, err := f.a.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	if source[0].State != "done" || source[0].Result != "landed" {
		t.Fatalf("source lost completion: %+v", source[0])
	}
	if err := f.b.core.ReceiveActivation(message); err != nil {
		t.Fatal(err)
	}
	imported, err = f.b.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0].State != "done" {
		t.Fatalf("replay restarted completed action: %+v", imported)
	}
}

func TestCancellationBeforeActivationPreservesAnUnexecutedTombstone(t *testing.T) {
	w := newOrgWorld(t)
	action := Activation{ID: "action", Kind: "directive", Payload: "Review work"}
	if err := w.core.ReceiveActivation(ActivationMessage{Operation: "cancel", Origin: "source", Action: action, Session: "session", Result: "obsolete"}); err != nil {
		t.Fatal(err)
	}
	if err := w.core.ReceiveActivation(ActivationMessage{Operation: "activate", Origin: "source", Action: action, Session: "session"}); err != nil {
		t.Fatal(err)
	}
	actions, err := w.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].State != "superseded" {
		t.Fatalf("cancelled action revived: %+v", actions)
	}
}

func TestRemoteRetryReplayDoesNotResetAnAlreadyAcceptedAction(t *testing.T) {
	w := newOrgWorld(t)
	action := Activation{ID: "action", Kind: "directive", Payload: "Review work"}
	if err := w.core.ReceiveActivation(ActivationMessage{Operation: "activate", Origin: "source", Action: action, Session: "session"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.db.Exec(`UPDATE ym_activations SET state='uncertain',target_session='session' WHERE id=?`, action.ID); err != nil {
		t.Fatal(err)
	}
	retry := ActivationMessage{Operation: "retry", Origin: "source", Action: action, Session: "session", Attempt: "explicit-retry"}
	if err := w.core.ReceiveActivation(retry); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.db.Exec(`UPDATE ym_activations SET state='claimed' WHERE id=?`, action.ID); err != nil {
		t.Fatal(err)
	}
	if err := w.core.ReceiveActivation(retry); err != nil {
		t.Fatal(err)
	}
	actions, err := w.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	if actions[0].State != "claimed" {
		t.Fatalf("retry replay reset dispatch: %+v", actions[0])
	}
}

func TestCourierDropKeepsUnknownAcceptanceVisibleAndExplicitRetryReoffersTheSameAction(t *testing.T) {
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "dropped before arrival", true: "receiver accepted before lost acknowledgement"}[accepted], func(t *testing.T) {
			f := newFleet(t)
			chief := f.c.chief(t, "operations")
			target := RemoteKey("c", chief.ID)
			if err := f.b.core.RequestDirective(target, "Review the retained work"); err != nil {
				t.Fatal(err)
			}
			actions, err := f.b.core.Store.Activations()
			if err != nil || len(actions) != 1 {
				t.Fatalf("source actions: %+v %v", actions, err)
			}
			original := actions[0]
			if _, err := f.b.core.transferActivation(original, target); err != nil {
				t.Fatal(err)
			}
			f.net.down["c"] = true
			f.sync(t)
			f.sync(t)
			pending, err := f.a.core.Store.PendingRelays()
			if err != nil {
				t.Fatal(err)
			}
			var held Relay
			var offer ActivationMessage
			for _, relay := range pending {
				if relay.Kind != RelayAction {
					continue
				}
				var message ActivationMessage
				if err := json.Unmarshal([]byte(relay.Body), &message); err != nil {
					t.Fatal(err)
				}
				if message.Operation == "activate" && message.Action.ID == original.ID {
					held, offer = relay, message
				}
			}
			if held.Seq == 0 {
				t.Fatal("courier did not retain the original activation")
			}
			stale := ActivationMessage{Operation: "dropped", Origin: "a", Destination: "different", Action: Activation{ID: original.ID}, Session: chief.ID, Result: "obsolete route"}
			if err := f.b.core.ReceiveActivation(stale); err != nil {
				t.Fatal(err)
			}
			stale.Destination, stale.Session = "c", "different-target"
			if err := f.b.core.ReceiveActivation(stale); err != nil {
				t.Fatal(err)
			}
			actions, err = f.b.core.Store.Activations()
			if err != nil || actions[0].State != "transferred" {
				t.Fatalf("obsolete notice changed source: %+v %v", actions, err)
			}
			if accepted {
				if err := f.c.core.ReceiveActivation(offer); err != nil {
					t.Fatal(err)
				}
				if err := f.c.core.DispatchActions(f.c.board(t)); err != nil {
					t.Fatal(err)
				}
				if len(f.c.handedTo(chief.Session)) != 1 {
					t.Fatal("fixture did not simulate native acceptance")
				}
			}
			if ok, err := f.a.core.Store.DropRelay(held.Seq); err != nil || !ok {
				t.Fatalf("courier drop: %v %v", ok, err)
			}
			pending, err = f.a.core.Store.PendingRelays()
			if err != nil {
				t.Fatal(err)
			}
			var notice ActivationMessage
			for _, relay := range pending {
				if relay.Kind == RelayAction {
					var message ActivationMessage
					if err := json.Unmarshal([]byte(relay.Body), &message); err != nil {
						t.Fatal(err)
					}
					if message.Operation == "dropped" {
						notice = message
					}
				}
			}
			if notice.Action.ID != original.ID || notice.Destination != "c" || notice.Session != chief.ID || notice.Origin != "a" {
				t.Fatalf("drop notice forged native receipt or lost route: %+v", notice)
			}
			f.net.loseReplies = 1
			for range 4 {
				f.sync(t)
			}
			actions, err = f.b.core.Store.Activations()
			if err != nil || actions[0].State != "uncertain" {
				t.Fatalf("source lost unknown acceptance: %+v %v", actions, err)
			}
			if err := f.b.core.ReceiveActivation(notice); err != nil {
				t.Fatal(err)
			}
			var cases int
			if err := f.b.core.Store.db.QueryRow(`SELECT count(*) FROM ym_items`).Scan(&cases); err != nil || cases != 1 {
				t.Fatalf("drop recovery case count %d: %v", cases, err)
			}
			if !accepted {
				arrived, err := f.c.core.Store.Activations()
				if err != nil || len(arrived) != 0 {
					t.Fatalf("unarrived activation appeared: %+v %v", arrived, err)
				}
			}
			if err := f.b.core.RetryAction(original.ID); err != nil {
				t.Fatal(err)
			}
			pending, err = f.b.core.Store.PendingRelays()
			if err != nil {
				t.Fatal(err)
			}
			var retry ActivationMessage
			offers := 0
			for _, relay := range pending {
				if relay.Kind == RelayAction {
					var message ActivationMessage
					if err := json.Unmarshal([]byte(relay.Body), &message); err != nil {
						t.Fatal(err)
					}
					if message.Action.ID == original.ID {
						if message.Operation == "activate" {
							offers++
						}
						if message.Operation == "retry" {
							retry = message
						}
					}
				}
			}
			if offers != 1 || retry.Attempt == "" {
				t.Fatalf("retry lost idempotent offer/token: offers=%d %+v", offers, retry)
			}
			f.net.down["c"] = false
			for range 5 {
				f.sync(t)
			}
			f.dispatch(t)
			for range 3 {
				f.sync(t)
			}
			if got := len(f.c.handedTo(chief.Session)); got != 1 {
				t.Fatalf("explicit recovery activated %d native turns, want once", got)
			}
			if err := f.c.core.ReceiveActivation(retry); err != nil {
				t.Fatal(err)
			}
			if err := f.c.core.ReceiveActivation(offer); err != nil {
				t.Fatal(err)
			}
			f.dispatch(t)
			if got := len(f.c.handedTo(chief.Session)); got != 1 {
				t.Fatalf("offer/retry replay activated %d native turns", got)
			}
			actions, err = f.b.core.Store.Activations()
			if err != nil || actions[0].State != "sent" {
				t.Fatalf("source did not receive native acceptance: %+v %v", actions, err)
			}
			if err := f.b.core.ReceiveActivation(notice); err != nil {
				t.Fatal(err)
			}
			actions, err = f.b.core.Store.Activations()
			if err != nil || actions[0].State != "sent" {
				t.Fatalf("late courier notice overrode native acceptance: %+v %v", actions, err)
			}
			if err := f.c.core.CompleteAction(original.ID, chief.Session, "landed"); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				f.sync(t)
			}
			if err := f.b.core.ReceiveActivation(notice); err != nil {
				t.Fatal(err)
			}
			actions, err = f.b.core.Store.Activations()
			if err != nil || actions[0].State != "done" {
				t.Fatalf("late courier notice overrode completion: %+v %v", actions, err)
			}
			var after int
			if err := f.b.core.Store.db.QueryRow(`SELECT count(*) FROM ym_items`).Scan(&after); err != nil || after != cases {
				t.Fatalf("late notices duplicated recovery cases: %d %v", after, err)
			}
		})
	}
}

func TestRemoteRetryObservesPendingAcceptedAndTerminalStates(t *testing.T) {
	for _, state := range []string{"pending", "sent", "done", "superseded"} {
		t.Run(state, func(t *testing.T) {
			w := newOrgWorld(t)
			action := Activation{ID: "action", Kind: "directive", Payload: "Review work"}
			if err := w.core.ReceiveActivation(ActivationMessage{Operation: "activate", Origin: "source", Action: action, Session: "session"}); err != nil {
				t.Fatal(err)
			}
			if _, err := w.core.Store.db.Exec(`UPDATE ym_activations SET state=?,target_session='session' WHERE id=?`, state, action.ID); err != nil {
				t.Fatal(err)
			}
			if err := w.core.ReceiveActivation(ActivationMessage{Operation: "retry", Origin: "source", Action: action, Session: "session", Attempt: "explicit-retry"}); err != nil {
				t.Fatal(err)
			}
			actions, err := w.core.Store.Activations()
			if err != nil || len(actions) != 1 || actions[0].State != state {
				t.Fatalf("retry replayed accepted state: %+v %v", actions, err)
			}
		})
	}
}

func TestFirstCourierNamingKeepsActivationIdentityAndUsesCanonicalReceiptRoutes(t *testing.T) {
	f := newFleet(t)
	chief := f.c.chief(t, "operations")
	if err := f.b.core.Store.setRelaySelf(RelaySelf{Name: "unnamed-source"}); err != nil {
		t.Fatal(err)
	}
	target := RemoteKey("c", chief.ID)
	if err := f.b.core.RequestDirective(target, "Review work after naming"); err != nil {
		t.Fatal(err)
	}
	actions, err := f.b.core.Store.Activations()
	if err != nil || len(actions) != 1 {
		t.Fatalf("source actions: %+v %v", actions, err)
	}
	original := actions[0]
	if _, err := f.b.core.transferActivation(original, target); err != nil {
		t.Fatal(err)
	}
	f.net.down["c"] = true
	f.sync(t)
	f.sync(t)
	pending, err := f.a.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	var held Relay
	for _, relay := range pending {
		if relay.Kind == RelayAction {
			var message ActivationMessage
			if err := json.Unmarshal([]byte(relay.Body), &message); err != nil {
				t.Fatal(err)
			}
			if message.Operation == "activate" {
				held = relay
				if message.Origin != "unnamed-source" || relay.Origin != "b" {
					t.Fatalf("fixture lost pre-naming body/canonical envelope: %+v %+v", message, relay)
				}
			}
		}
	}
	if held.Seq == 0 {
		t.Fatal("courier did not retain activation")
	}
	if err := f.c.core.actOn(f.c.board(t), nil, held); err != nil {
		t.Fatal(err)
	}
	var origin string
	if err := f.c.core.Store.db.QueryRow(`SELECT origin FROM ym_activation_origins WHERE id=?`, original.ID).Scan(&origin); err != nil || origin != "b" {
		t.Fatalf("receiver trusted stale body origin: %q %v", origin, err)
	}
	receipts, err := f.c.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range receipts {
		if receipt.Kind == RelayAction && receipt.Dest != "b" {
			t.Fatalf("receipt used unreachable pre-naming alias: %+v", receipt)
		}
	}
	if ok, err := f.a.core.Store.DropRelay(held.Seq); err != nil || !ok {
		t.Fatalf("drop held pre-naming activation: %v %v", ok, err)
	}
	for range 3 {
		f.sync(t)
	}
	actions, err = f.b.core.Store.Activations()
	if err != nil || actions[0].State != "uncertain" {
		t.Fatalf("drop notice missed canonical source: %+v %v", actions, err)
	}
	f.net.down["c"] = false
	for range 3 {
		f.sync(t)
	}
	f.dispatch(t)
	for range 3 {
		f.sync(t)
	}
	if err := f.c.core.actOn(f.c.board(t), nil, held); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t)
	if got := len(f.c.handedTo(chief.Session)); got != 1 {
		t.Fatalf("canonicalization changed stable action identity: native turns=%d", got)
	}
	actions, err = f.b.core.Store.Activations()
	if err != nil || len(actions) != 1 || actions[0].ID != original.ID || actions[0].State != "sent" {
		t.Fatalf("source lost canonical receipt: %+v %v", actions, err)
	}
}
