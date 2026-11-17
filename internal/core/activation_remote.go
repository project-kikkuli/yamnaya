package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"strings"
)

const RelayAction = "action"
const activationRemoteSchema = `CREATE TABLE IF NOT EXISTS ym_activation_origins (
 id TEXT PRIMARY KEY, origin TEXT NOT NULL DEFAULT '', destination TEXT NOT NULL DEFAULT '',
 target_session TEXT NOT NULL DEFAULT '', cancelled INTEGER NOT NULL DEFAULT 0,
 receipt_seq INTEGER NOT NULL DEFAULT 0, received_seq INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS ym_activation_retries(token TEXT PRIMARY KEY,activation TEXT NOT NULL);`

type ActivationMessage struct {
	Destination string     `json:"destination,omitempty"`
	Revision    int64      `json:"revision,omitempty"`
	Operation   string     `json:"operation"`
	Origin      string     `json:"origin"`
	Action      Activation `json:"action"`
	Session     string     `json:"session,omitempty"`
	State       string     `json:"state,omitempty"`
	Result      string     `json:"result,omitempty"`
	Attempt     string     `json:"attempt,omitempty"`
}

func (c *Core) importedActivation(id string) (bool, error) {
	var origin string
	err := c.Store.db.QueryRow(`SELECT origin FROM ym_activation_origins WHERE id=?`, id).Scan(&origin)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return origin != "", err
}

func (c *Core) transferActivation(a Activation, session string) (bool, error) {
	host, target, remote := SplitRemote(session)
	if !remote {
		return c.returnActivationLocal(a, session)
	}
	imported, err := c.importedActivation(a.ID)
	if err != nil {
		return true, err
	}
	if imported {
		return true, errors.New("imported supervisor actions cannot be forwarded")
	}
	origin := c.SelfName()
	tx, err := c.Store.db.Begin()
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	var destination, previousTarget string
	err = tx.QueryRow(`SELECT destination,target_session FROM ym_activation_origins WHERE id=?`, a.ID).Scan(&destination, &previousTarget)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return true, err
	}
	if destination == host && previousTarget == target {
		return true, nil
	}
	if destination != "" {
		if err = c.activationRelayIn(tx, destination, ActivationMessage{Operation: "cancel", Origin: origin, Action: Activation{ID: a.ID}, Session: previousTarget, Result: "supervisor session moved"}); err != nil {
			return true, err
		}
	}
	message := ActivationMessage{Operation: "activate", Origin: origin, Action: a, Session: target}
	raw, err := json.Marshal(message)
	if err != nil {
		return true, err
	}
	if _, err = tx.Exec(`INSERT INTO ym_relay(origin,origin_seq,dest,kind,body,created) VALUES('',0,?,?,?,?)`, host, RelayAction, string(raw), c.now().UnixNano()); err != nil {
		return true, err
	}
	if _, err = tx.Exec(`INSERT INTO ym_activation_origins(id,destination,target_session) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET destination=excluded.destination,target_session=excluded.target_session,received_seq=0`, a.ID, host, target); err != nil {
		return true, err
	}
	if _, err = tx.Exec(`UPDATE ym_activations SET state='transferred',target_session=?,error='' WHERE id=?`, session, a.ID); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

func (c *Core) activationReceipt(a Activation, state, result string) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = c.activationReceiptIn(tx, a, state, result); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Core) activationReceiptIn(tx *sql.Tx, a Activation, state, result string) error {
	var origin, target string
	err := tx.QueryRow(`SELECT origin,target_session FROM ym_activation_origins WHERE id=?`, a.ID).Scan(&origin, &target)
	if errors.Is(err, sql.ErrNoRows) || err == nil && origin == "" {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE ym_activation_origins SET receipt_seq=receipt_seq+1 WHERE id=?`, a.ID); err != nil {
		return err
	}
	var revision int64
	if err = tx.QueryRow(`SELECT receipt_seq FROM ym_activation_origins WHERE id=?`, a.ID).Scan(&revision); err != nil {
		return err
	}
	return c.activationRelayIn(tx, origin, ActivationMessage{Operation: "receipt", Revision: revision, Origin: activationSelfIn(tx), Action: Activation{ID: a.ID}, Session: target, State: state, Result: result})
}

func (c *Core) cancelTransferredActivation(a Activation) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = c.cancelTransferredActivationIn(tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Core) cancelTransferredActivationIn(tx *sql.Tx, a Activation) error {
	var destination, target string
	err := tx.QueryRow(`SELECT destination,target_session FROM ym_activation_origins WHERE id=?`, a.ID).Scan(&destination, &target)
	if errors.Is(err, sql.ErrNoRows) || err == nil && destination == "" {
		return nil
	}
	if err != nil {
		return err
	}
	return c.activationRelayIn(tx, destination, ActivationMessage{Operation: "cancel", Origin: activationSelfIn(tx), Action: Activation{ID: a.ID}, Session: target, Result: "source facts no longer require action"})
}

func (c *Core) activationRelayIn(tx *sql.Tx, destination string, m ActivationMessage) error {
	raw, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT INTO ym_relay(origin,origin_seq,dest,kind,body,created) VALUES('',0,?,?,?,?)`, destination, RelayAction, string(raw), c.now().UnixNano())
	return err
}

func (c *Core) ReceiveActivation(m ActivationMessage) error {
	if m.Action.ID == "" || strings.TrimSpace(m.Origin) == "" {
		return Final(errors.New("supervisor action identity and origin required"))
	}
	switch m.Operation {
	case "activate":
		return c.receiveActivation(m)
	case "retry":
		return c.receiveActivationRetry(m)
	case "dropped":
		return c.receiveActivationDrop(m)
	case "cancel":
		tx, err := c.Store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var origin, target string
		err = tx.QueryRow(`SELECT origin,target_session FROM ym_activation_origins WHERE id=?`, m.Action.ID).Scan(&origin, &target)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && origin != m.Origin {
			return Final(errors.New("supervisor cancellation origin mismatch"))
		}
		if target != "" && m.Session != "" && target != m.Session {
			return nil
		}
		if _, err = tx.Exec(`INSERT INTO ym_activation_origins(id,origin,target_session,cancelled) VALUES(?,?,?,1) ON CONFLICT(id) DO UPDATE SET cancelled=1`, m.Action.ID, m.Origin, m.Session); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE ym_activations SET state='superseded',result=?,error='' WHERE id=? AND state NOT IN ('done','superseded')`, m.Result, m.Action.ID); err != nil {
			return err
		}
		if err = c.activationReceiptIn(tx, m.Action, "superseded", m.Result); err != nil {
			return err
		}
		return tx.Commit()
	case "receipt":
		tx, err := c.Store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var destination, target string
		var revision int64
		if err = tx.QueryRow(`SELECT destination,target_session,received_seq FROM ym_activation_origins WHERE id=?`, m.Action.ID).Scan(&destination, &target, &revision); err != nil {
			return err
		}
		if destination != m.Origin || target != m.Session || m.Revision <= revision {
			return nil
		}

		switch m.State {
		case "pending", "claimed", "sent", "blocked", "uncertain", "done", "superseded":
		default:
			return Final(fmt.Errorf("invalid supervisor receipt state %q", m.State))
		}
		state := m.State
		if state == "pending" || state == "claimed" {
			state = "transferred"
		}
		why := ""
		if state == "blocked" || state == "uncertain" {
			why = m.Result
		}
		if _, err = tx.Exec(`UPDATE ym_activation_origins SET received_seq=? WHERE id=?`, m.Revision, m.Action.ID); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE ym_activations SET state=?,result=?,error=? WHERE id=? AND state NOT IN ('done','superseded')`, state, m.Result, why, m.Action.ID); err != nil {
			return err
		}
		if state == "done" || state == "superseded" || state == "sent" {
			a := Activation{ID: m.Action.ID}
			if err = tx.QueryRow(`SELECT kind,subject,attention FROM ym_activations WHERE id=?`, a.ID).Scan(&a.Kind, &a.Subject, &a.Attention); err != nil {
				return err
			}
			if state == "done" && a.Kind == "answer" {
				if err = settleAnswerIn(tx, a, c.now()); err != nil {
					return err
				}
			}
			if err = settleActivationAttentionIn(tx, a, c.now()); err != nil {
				return err
			}
		}

		if err = tx.Commit(); err != nil {
			return err
		}

		if state == "blocked" || state == "uncertain" {
			actions, err := c.Store.Activations()
			if err != nil {
				return err
			}
			for _, action := range actions {
				if action.ID == m.Action.ID && action.State == state {
					return c.actionAttention(action, why)
				}
			}
		}
		return nil
	default:
		return Final(fmt.Errorf("unknown supervisor operation %q", m.Operation))
	}
}

func (c *Core) receiveActivation(m ActivationMessage) error {
	if m.Session == "" || strings.TrimSpace(m.Action.Payload) == "" {
		return Final(errors.New("supervisor action session and payload required"))
	}
	if _, _, remote := SplitRemote(m.Session); remote {
		return Final(errors.New("supervisor action destination must be local"))
	}
	org, err := c.LoadOrg()
	if err != nil {
		return err
	}
	recipient, boundSession := m.Session, m.Session
	if manager, ok := org.Managers[m.Session]; ok {
		recipient, boundSession = manager.ID, manager.Session
	} else if manager, ok := org.ManagerOf(m.Session); ok {
		recipient = manager.ID
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var origin, previousTarget string
	var cancelled bool
	err = tx.QueryRow(`SELECT origin,target_session,cancelled FROM ym_activation_origins WHERE id=?`, m.Action.ID).Scan(&origin, &previousTarget, &cancelled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && origin != m.Origin {
		return Final(errors.New("supervisor activation origin mismatch"))
	}
	retarget := previousTarget != "" && previousTarget != m.Session
	if retarget {
		cancelled = false
	}
	if _, err = tx.Exec(`INSERT INTO ym_activation_origins(id,origin,target_session) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET target_session=excluded.target_session,cancelled=CASE WHEN target_session<>excluded.target_session THEN 0 ELSE cancelled END`, m.Action.ID, m.Origin, m.Session); err != nil {
		return err
	}
	state := "pending"
	if cancelled {
		state = "superseded"
	}
	if _, err = tx.Exec(`INSERT INTO ym_activations(id,recipient,session,kind,subject,payload,state,created) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`, m.Action.ID, recipient, boundSession, m.Action.Kind, m.Action.Subject, m.Action.Payload, state, stamp(c.now())); err != nil {
		return err
	}
	if retarget {
		if _, err = tx.Exec(`UPDATE ym_activations SET recipient=?,session=?,state='pending',target_session='',error='',result='' WHERE id=?`, recipient, boundSession, m.Action.ID); err != nil {
			return err
		}
	}
	var current, result string
	if err = tx.QueryRow(`SELECT state,CASE WHEN error<>'' THEN error ELSE result END FROM ym_activations WHERE id=?`, m.Action.ID).Scan(&current, &result); err != nil {
		return err
	}
	if err = c.activationReceiptIn(tx, m.Action, current, result); err != nil {
		return err
	}
	return tx.Commit()
}

func activationSelfIn(tx *sql.Tx) string {
	var name string
	if err := tx.QueryRow(`SELECT name FROM ym_relay_self WHERE id=1`).Scan(&name); err == nil && name != "" {
		return name
	}
	return osHostName()
}

func (c *Core) retryTransferredActivation(id string) (bool, error) {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	var destination, state, target string
	action := Activation{ID: id}
	err = tx.QueryRow(`SELECT o.destination,a.state,o.target_session,a.kind,a.subject,a.payload FROM ym_activation_origins o JOIN ym_activations a ON a.id=o.id WHERE o.id=?`, id).Scan(&destination, &state, &target, &action.Kind, &action.Subject, &action.Payload)
	if errors.Is(err, sql.ErrNoRows) || err == nil && destination == "" {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if state != "blocked" && state != "uncertain" && state != "claimed" {
		return true, errors.New("remote action is not awaiting recovery")
	}
	origin := activationSelfIn(tx)
	if err = c.activationRelayIn(tx, destination, ActivationMessage{Operation: "activate", Origin: origin, Action: action, Session: target}); err != nil {
		return true, err
	}
	token := uuid.NewString()
	if err = c.activationRelayIn(tx, destination, ActivationMessage{Operation: "retry", Origin: activationSelfIn(tx), Action: Activation{ID: id}, Session: target, Attempt: token}); err != nil {
		return true, err
	}
	if _, err = tx.Exec(`UPDATE ym_activations SET state='transferred',error='' WHERE id=?`, id); err != nil {
		return true, err
	}
	return true, tx.Commit()
}

func (c *Core) receiveActivationRetry(m ActivationMessage) error {
	if m.Attempt == "" {
		return Final(errors.New("supervisor retry identity required"))
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var origin, target string
	if err = tx.QueryRow(`SELECT origin,target_session FROM ym_activation_origins WHERE id=?`, m.Action.ID).Scan(&origin, &target); err != nil {
		return err
	}
	if origin != m.Origin {
		return Final(errors.New("supervisor retry origin mismatch"))
	}
	if target != m.Session {
		return nil
	}
	var state, detail string
	if err = tx.QueryRow(`SELECT state,CASE WHEN error<>'' THEN error ELSE result END FROM ym_activations WHERE id=?`, m.Action.ID).Scan(&state, &detail); err != nil {
		return err
	}
	result, err := tx.Exec(`INSERT INTO ym_activation_retries(token,activation) VALUES(?,?) ON CONFLICT(token) DO NOTHING`, m.Attempt, m.Action.ID)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted > 0 && (state == "blocked" || state == "uncertain" || state == "claimed") {
		if err = retryActivationIn(tx, m.Action.ID); err != nil {
			return err
		}
		state, detail = "pending", ""
	}
	if err = c.activationReceiptIn(tx, m.Action, state, detail); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Core) AcceptAction(id, session string) error {
	session = c.Resolve(session)
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var target, state string
	if err = tx.QueryRow(`SELECT target_session,state FROM ym_activations WHERE id=?`, id).Scan(&target, &state); err != nil {
		return err
	}
	if target != session || state != "claimed" && state != "uncertain" && state != "sent" {
		return errors.New("only the accepting supervisor session can acknowledge its current action")
	}
	if _, err = tx.Exec(`UPDATE ym_activations SET state='sent',error='' WHERE id=?`, id); err != nil {
		return err
	}
	a := Activation{ID: id}
	if err = tx.QueryRow(`SELECT attention FROM ym_activations WHERE id=?`, id).Scan(&a.Attention); err != nil {
		return err
	}
	if err = settleActivationAttentionIn(tx, a, c.now()); err != nil {
		return err
	}
	if err = c.activationReceiptIn(tx, Activation{ID: id}, "sent", ""); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Core) RequestDirective(session, text string) error {
	return c.requestAction(Board{}, c.Resolve(session), "directive", "", text)
}

func (c *Core) returnActivationLocal(a Activation, session string) (bool, error) {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var origin, destination, target string
	err = tx.QueryRow(`SELECT origin,destination,target_session FROM ym_activation_origins WHERE id=?`, a.ID).Scan(&origin, &destination, &target)
	if errors.Is(err, sql.ErrNoRows) || err == nil && (origin != "" || destination == "") {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err = c.activationRelayIn(tx, destination, ActivationMessage{Operation: "cancel", Origin: activationSelfIn(tx), Action: Activation{ID: a.ID}, Session: target, Result: "supervisor session moved"}); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE ym_activation_origins SET destination='',target_session=?,received_seq=0 WHERE id=?`, session, a.ID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE ym_activations SET state='pending',target_session='',error='' WHERE id=?`, a.ID); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

func activationDropIn(tx *sql.Tx, m ActivationMessage) (bool, error) {
	if m.Destination == "" || m.Session == "" {
		return false, Final(errors.New("dropped activation needs its expected destination and target"))
	}
	result, err := tx.Exec(`UPDATE ym_activations SET state='uncertain',error=? WHERE id=? AND state='transferred' AND EXISTS (SELECT 1 FROM ym_activation_origins o WHERE o.id=ym_activations.id AND o.origin='' AND o.destination=? AND o.target_session=?)`, m.Result, m.Action.ID, m.Destination, m.Session)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

func (c *Core) receiveActivationDrop(m ActivationMessage) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := activationDropIn(tx, m)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	actions, err := c.Store.Activations()
	if err != nil {
		return err
	}
	for _, action := range actions {
		if action.ID == m.Action.ID && action.State == "uncertain" {
			return c.actionAttention(action, m.Result)
		}
	}
	return nil
}
