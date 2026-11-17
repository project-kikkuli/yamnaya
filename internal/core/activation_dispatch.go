package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

func (c *Core) dispatchNativeActions(b Board, o Org, s Live, first Activation, actions []Activation) error {
	batch := []Activation{first}
	bytes := len(first.Payload)
	for _, a := range actions {
		if len(batch) >= 16 {
			break
		}
		if a.ID == first.ID || a.State != "pending" || bytes+len(a.Payload) > 16*1024 {
			continue
		}
		session := c.carrier(o, a.Session)
		if m, ok := o.Managers[a.Recipient]; ok {
			if !o.Active(m) {
				continue
			}
			session = m.Session
		}
		if session != s.Key() {
			continue
		}
		needed, err := c.actionNeeded(b, o, a)
		if err != nil {
			return err
		}
		if needed {
			a, err = c.refreshQuestionPayload(b, o, a, session)
			if err != nil {
				return err
			}
			batch = append(batch, a)
			bytes += len(a.Payload)
		}
	}
	if err := c.CheckLaunch(NewLaunch{Repo: s.Cwd, Harness: s.Harness, Profile: s.Profile}); err != nil {
		if errors.Is(err, ErrLaunchDeferred) {
			return c.deferNativeActions(batch, err.Error())
		}
		return err
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var active int
	if err = tx.QueryRow(`SELECT count(*) FROM ym_activations WHERE target_session=? AND state IN ('claimed','sent','blocked','uncertain')`, s.Key()).Scan(&active); err != nil {
		return err
	}
	if active != 0 {
		return nil
	}
	var claimed []Activation
	for _, a := range batch {
		err = tx.QueryRow(`UPDATE ym_activations SET state='claimed',target_session=?,dispatched=?,attempt=attempt+1,error='' WHERE id=? AND state='pending' RETURNING attempt`, s.Key(), stamp(c.now()), a.ID).Scan(&a.Attempt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		claimed = append(claimed, a)
	}
	var members []string
	for _, a := range claimed {
		members = append(members, a.ID+"/"+strconv.Itoa(a.Attempt))
	}
	batchID := activationID(s.Key(), "submission", strings.Join(members, ","))
	for i, a := range claimed {
		claimed[i].Batch = batchID
		if _, err = tx.Exec(`UPDATE ym_activations SET batch=? WHERE id=?`, batchID, a.ID); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if len(claimed) == 0 {
		return nil
	}
	var payload strings.Builder
	for _, a := range claimed {
		fmt.Fprintf(&payload, "Yamnaya supervisor action %s (attempt %d, %s), for %s; source: Yamnaya control plane, not the human owner.\n%s\nRecord this action's result with ym action complete %s --result <what changed>, or acknowledge it with ym action accept %s while working.\n\n", a.ID, a.Attempt, a.Kind, c.RefName(a.Recipient), a.Payload, a.ID, a.ID)
	}
	if c.BackgroundSend != nil {
		err = c.BackgroundSend(s, payload.String())
	} else {
		err = c.message(s.Session, payload.String(), false)
	}
	state, why := "sent", ""
	if err != nil {
		state, why = "blocked", err.Error()
		if errors.Is(err, ErrSubmissionUncertain) {
			state = "uncertain"
		}
	}
	tx, err = c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var recovery []Activation
	for _, a := range claimed {
		result, err := tx.Exec(`UPDATE ym_activations SET state=?,error=? WHERE id=? AND state='claimed' AND attempt=?`, state, why, a.ID, a.Attempt)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed == 0 {
			continue
		}
		if err = c.activationReceiptIn(tx, a, state, why); err != nil {
			return err
		}
		if why != "" {
			recovery = append(recovery, a)
		} else if err = settleActivationAttentionIn(tx, a, c.now()); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	for _, a := range recovery {
		if err = c.actionAttention(a, why); err != nil {
			return err
		}
	}
	return nil
}

func (c *Core) deferNativeActions(batch []Activation, reason string) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, action := range batch {
		result, err := tx.Exec(`UPDATE ym_activations SET error=? WHERE id=? AND state='pending' AND error<>?`, reason, action.ID, reason)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed > 0 {
			if err = c.activationReceiptIn(tx, action, "pending", reason); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
