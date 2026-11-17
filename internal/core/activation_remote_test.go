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
