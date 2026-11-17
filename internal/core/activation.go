package core

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const activationSchema = `
CREATE TABLE IF NOT EXISTS ym_activations (
 id TEXT PRIMARY KEY, recipient TEXT NOT NULL, session TEXT NOT NULL,
 kind TEXT NOT NULL, subject TEXT NOT NULL, payload TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending', created TEXT NOT NULL,
 dispatched TEXT, target_session TEXT NOT NULL DEFAULT '',
 error TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '',
 attention INTEGER NOT NULL DEFAULT 0, attention_attempt INTEGER NOT NULL DEFAULT 0, attempt INTEGER NOT NULL DEFAULT 0, batch TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ym_activations_pending ON ym_activations(state,recipient);
CREATE INDEX IF NOT EXISTS ym_activations_target ON ym_activations(target_session,state);
`

type Activation struct {
	Batch                                                 string
	Attempt, AttentionAttempt                             int
	ID, Recipient, Session, Kind, Subject, Payload, State string
	Created, Dispatched                                   time.Time
	TargetSession, Error, Result                          string
	Attention                                             int64
}

func activationID(recipient, kind, subject string) string {
	h := sha256.Sum256([]byte(recipient + "\x00" + kind + "\x00" + subject))
	return hex.EncodeToString(h[:16])
}

func (s *Store) Activations() ([]Activation, error) { return s.activations("") }

func (s *Store) activeActivations() ([]Activation, error) {
	return s.activations(" WHERE state IN ('pending','claimed','sent','blocked','uncertain','transferred')")
}

func (s *Store) activations(where string) ([]Activation, error) {
	rows, err := s.db.Query(`SELECT id,recipient,session,kind,subject,payload,state,created,dispatched,target_session,error,result,attention,attempt,batch,attention_attempt FROM ym_activations` + where + ` ORDER BY created,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Activation
	for rows.Next() {
		var a Activation
		var created sql.NullString
		var dispatched sql.NullString
		if err := rows.Scan(&a.ID, &a.Recipient, &a.Session, &a.Kind, &a.Subject, &a.Payload, &a.State, &created, &dispatched, &a.TargetSession, &a.Error, &a.Result, &a.Attention, &a.Attempt, &a.Batch, &a.AttentionAttempt); err != nil {
			return nil, err
		}
		a.Created, a.Dispatched = parseTime(created), parseTime(dispatched)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (c *Core) requestAction(b Board, session, kind, subject, text string) error {
	if strings.TrimSpace(text) == "" {
		return errors.New("a supervisor action needs a requested action")
	}
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	session = c.carrier(o, session)
	recipient := session
	if m, ok := o.ManagerOf(session); ok {
		recipient = m.ID
	}
	if subject == "" {
		subject = text
	}
	machine, err := c.Store.MachineID()
	if err != nil {
		return err
	}
	_, err = c.Store.db.Exec(`INSERT INTO ym_activations(id,recipient,session,kind,subject,payload,created) VALUES(?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		activationID(RemoteKey(machine, recipient), kind, subject), recipient, session, kind, subject, c.Store.Nicked(text), stamp(c.now()))
	return err
}

var activationPR = regexp.MustCompile(`https://github\.com/[^\s"/]+/[^\s"/]+/pull/[0-9]+`)

func (c *Core) eventActionNeeded(b Board, o Org, e GoalEvent) (bool, error) {
	if g, ok := o.Goals[e.Goal]; ok && g.Ended() {
		return false, nil
	}
	switch e.Kind {
	case EventIdleAgain, EventUpdate, EventPROpened, EventPRTouched, EventCIGreen, EventMerged, EventClosed, EventDone, EventVerified, EventVerifying, EventAdopted, EventRefreshed, EventResumed, EventWorkResumed, EventMachineThrottled, EventNudge, EventRelayed, EventGoalClosed, EventGoalArchived, EventSteered:
		return false, nil
	case EventAsked:
		return false, nil
	case EventCIRed, EventConflict, EventReview, EventPRReady:
		url := activationPR.FindString(e.Payload)
		if url == "" {
			return false, nil
		}
		statuses, err := c.Store.PRStatuses()
		if err != nil {
			return false, err
		}
		st, ok := statuses[url]
		if !ok || st.State != "OPEN" {
			return false, nil
		}
		if e.Key != "" && st.Merge.OID != "" && strings.Contains(e.Key, "@") && !strings.Contains(e.Key, "@"+st.Merge.OID) {
			return false, nil
		}
		switch e.Kind {
		case EventCIRed:
			return st.Failed > 0 && st.Pending == 0, nil
		case EventConflict:
			return st.Merge.Mergeable == "CONFLICTING", nil
		case EventReview:
			return st.Unresolved > 0 || st.Review == "CHANGES_REQUESTED", nil
		default:
			cleared, err := c.Store.CanMerge(prRepoName(PR{URL: url, Repo: strings.TrimPrefix(strings.Split(url, "/pull/")[0], "https://github.com/")}))
			return cleared && st.ReadyToMerge(c.now()), err
		}
	case EventStuck:
		g, err := c.Store.Goal(e.Goal)
		return err == nil && !g.Ended() && g.State == GoalStuck && !strings.HasPrefix(g.Evidence, quietEvidence), err
	case EventFailedVerify:
		if strings.Contains(e.Payload, " withdrawn: ") {
			return false, nil
		}
		g, err := c.Store.Goal(e.Goal)
		if err != nil || g.Ended() || g.State == GoalVerifying {
			return false, err
		}
		verifications, err := c.Store.Verifications(e.Goal)
		if err != nil || len(verifications) == 0 {
			return false, err
		}
		v := verifications[0]
		for _, candidate := range verifications {
			if candidate.ID > v.ID {
				v = candidate
			}
		}
		return (v.Verdict == VerdictFail || v.Verdict == VerdictError) && v.WorkRevision == g.WorkRevision && !v.DecidedAt.After(e.Created), nil
	case EventTimer, EventGoalReopened:
		return true, nil
	case EventRelayFailed:
		pending, err := c.Store.PendingRelays()
		if err != nil {
			return false, err
		}
		for _, relay := range pending {
			if relay.Error != "" && strings.Contains(e.Payload, relay.Error) {
				return true, nil
			}
		}
		return false, nil
	case EventFinished:
		assignments, err := c.Store.Assignments(e.Goal)
		if err != nil {
			return false, err
		}
		for _, a := range assignments {
			if a.Report == e.Seq {
				return a.State == "outstanding" && a.AcceptedReport == 0, nil
			}
		}
		return !strings.HasPrefix(e.Payload, "assignment "), nil
	case EventAgentSwollen:
		return c.swollenActionNeeded(b, e)
	case EventAgentAdrift:
		return c.driftActionNeeded(b, e)
	case EventAgentStopped, EventAgentHung, EventAgentOnboarding, EventResumeFailed:
		fields := strings.Fields(e.Payload)
		if len(fields) < 2 {
			return false, nil
		}
		ref := strings.TrimSuffix(fields[1], "'s")
		for _, s := range b.Sessions {
			if ref != Short(s.Key()) && !SessionMatches(s.Key(), ref) {
				continue
			}
			switch e.Kind {
			case EventAgentStopped, EventResumeFailed:
				return false, nil
			case EventAgentHung:
				return s.Hung(c.now(), c.Woke), nil
			case EventAgentOnboarding:
				return s.Status == Onboarding, nil

			}
		}
		if e.Kind != EventAgentStopped && e.Kind != EventResumeFailed {
			return false, nil
		}
		if strings.Contains(e.Payload, "exited with status 0") {
			return false, nil
		}
		history, err := c.Store.GoalEvents(e.Goal)
		if err != nil || cameBack(history, e) {
			return false, err
		}
		for _, l := range b.Launched {
			if SessionMatches(l.Session, ref) && !l.MayRun() && l.Landed.IsZero() && l.Killed.IsZero() {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, nil
	}
}

func (c *Core) actionNeeded(b Board, o Org, a Activation) (bool, error) {
	imported, err := c.importedActivation(a.ID)
	if err != nil || imported {
		return imported, err
	}
	if a.Kind == "answer" {
		return c.answerActionNeeded(a)
	}
	if a.Kind == "relay-failure" {
		parts := strings.SplitN(a.Subject, "@", 2)
		if len(parts) != 2 {
			return false, errors.New("relay failure needs its source refusal")
		}
		seq, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return false, err
		}
		return c.Store.relayFailureCurrent(seq, parts[1])
	}
	if a.Kind == "verdict" {
		parts := strings.SplitN(a.Subject, ":", 2)
		if len(parts) != 2 {
			return false, errors.New("verdict action needs its completion attempt")
		}
		v, err := c.Store.OpenVerification(parts[0])
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return err == nil && v.Session == a.Session && strconv.Itoa(v.Attempt) == parts[1], err
	}
	if a.Kind == "event" {
		seq, err := strconv.ParseInt(a.Subject, 10, 64)
		if err != nil {
			return false, err
		}
		e, err := c.Store.GoalEvent(seq)
		if err != nil {
			return false, err
		}
		return c.eventActionNeeded(b, o, e)
	}
	if a.Kind == "question" {
		parts := strings.SplitN(a.Subject, "@", 2)
		it, err := c.Store.Item(parts[0])
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(parts) == 2 && strconv.Itoa(len(it.History)) != parts[1] {
			return false, nil
		}
		return it.State == ItemOpen && it.Recipient == a.Recipient, nil
	}
	if a.Kind == "merge" {
		statuses, err := c.Store.PRStatuses()
		parts := strings.SplitN(a.Subject, "@", 2)
		st := statuses[parts[0]]
		return st.ReadyToMerge(c.now()) && (len(parts) == 1 || st.Merge.OID == parts[1]), err
	}
	if a.Kind == "adopt" {
		steers, err := c.Store.LatestSteers()
		st, ok := steers[a.Recipient]
		return ok && !st.Adopted() && strconv.Itoa(st.Revision) == a.Subject, err
	}
	return true, nil
}

func (c *Core) actionAttention(a Activation, why string) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = tx.QueryRow(`SELECT attention,attempt,attention_attempt,recipient,error FROM ym_activations WHERE id=?`, a.ID).Scan(&a.Attention, &a.Attempt, &a.AttentionAttempt, &a.Recipient, &a.Error); err != nil {
		return err
	}
	if a.Attention != 0 && a.AttentionAttempt == a.Attempt && a.Error == why {
		return nil
	}
	attention := a.Attention
	if attention != 0 {
		if a.AttentionAttempt != a.Attempt {
			if err = reopenRecoveryAttentionIn(tx, attention, why); err != nil {
				return err
			}
		}
	} else {
		err = tx.QueryRow(`SELECT x.attention FROM ym_activations x JOIN ym_items i ON i.seq=x.attention WHERE x.recipient=? AND i.state IN ('open','snoozed') ORDER BY x.created LIMIT 1`, a.Recipient).Scan(&attention)
		if errors.Is(err, sql.ErrNoRows) {
			it, addErr := addItemIn(tx, Item{Kind: KindChore, Session: "ym", Recipient: Owner,
				Question: "Supervisor control needs recovery for " + a.Recipient + ": " + why,
				Rec:      "Open Y on the board to inspect supervisor recovery and the original request, then explicitly retry after checking the connection and prior effects. CLI: ym action list; ym action retry <id>.",
				DoLine:   "Recover supervisor control", Impact: ImpactAgents,
				Evidence: []string{"ym action list", "ym action show " + a.ID}})
			if addErr != nil {
				return addErr
			}
			attention = it.Seq
		} else if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`UPDATE ym_activations SET attention=?,attention_attempt=attempt,error=? WHERE id=?`, attention, why, a.ID); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		c.Store.invalidate()
	}
	return err
}

func reopenRecoveryAttentionIn(tx *sql.Tx, seq int64, why string) error {
	var previous string
	if err := tx.QueryRow(`SELECT state FROM ym_items WHERE seq=?`, seq).Scan(&previous); err != nil {
		return err
	}
	if previous == ItemOpen || previous == ItemSnoozed {
		return nil
	}
	if _, err := tx.Exec(`UPDATE ym_items SET state='open',answer='',reason='',answered_at=NULL,delivered_at=NULL,held_at=NULL,snooze_until=NULL WHERE seq=?`, seq); err != nil {
		return err
	}
	return addEvent(tx, fmt.Sprintf("#%d", seq), previous, ItemOpen, why)
}

func migrateActivationAttention(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var has int
	if err = tx.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_activations') WHERE name='attention_attempt'`).Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		if _, err = tx.Exec(`ALTER TABLE ym_activations ADD COLUMN attention_attempt INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE ym_activations SET attention_attempt=attempt WHERE attention<>0`); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Core) finishActivation(a Activation, state, result string) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if state == "done" {
		var target, current string
		if err = tx.QueryRow(`SELECT target_session,state FROM ym_activations WHERE id=?`, a.ID).Scan(&target, &current); err != nil {
			return err
		}
		if target != a.TargetSession || current != "sent" && current != "claimed" && current != "uncertain" {
			return errors.New("supervisor action changed before completion")
		}
	}
	if _, err = tx.Exec(`UPDATE ym_activations SET state=?,result=?,error='' WHERE id=? AND state NOT IN ('done','superseded')`, state, result, a.ID); err != nil {
		return err
	}
	if state == "done" && a.Kind == "answer" {
		if err = settleAnswerIn(tx, a, c.now()); err != nil {
			return err
		}
	}
	if err = c.activationReceiptIn(tx, a, state, result); err != nil {
		return err
	}
	if state == "superseded" {
		if err = c.cancelTransferredActivationIn(tx, a); err != nil {
			return err
		}
	}
	if err = settleActivationAttentionIn(tx, a, c.now()); err != nil {
		return err
	}

	return tx.Commit()
}

func settleActivationAttentionIn(tx *sql.Tx, a Activation, at time.Time) error {
	var err error
	if a.Attention != 0 {
		var outstanding int
		if err = tx.QueryRow(`SELECT count(*) FROM ym_activations WHERE attention=? AND (state NOT IN ('done','superseded','sent') OR error<>'')`, a.Attention).Scan(&outstanding); err != nil {
			return err
		}
		if outstanding == 0 {
			var previous string
			if err = tx.QueryRow(`SELECT state FROM ym_items WHERE seq=?`, a.Attention).Scan(&previous); err != nil {
				return err
			}
			if previous == ItemOpen || previous == ItemSnoozed {
				if _, err = tx.Exec(`UPDATE ym_items SET state=?,reason=?,answered_at=?,delivered_at=? WHERE seq=?`, ItemDismissed, "supervisor control recovered", stamp(at), stamp(at), a.Attention); err != nil {
					return err
				}
				if err = addEvent(tx, fmt.Sprintf("#%d", a.Attention), previous, ItemDismissed, "supervisor control recovered"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *Core) DispatchActions(b Board) error {
	actions, err := c.Store.activeActivations()
	if err != nil {
		return err
	}
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	used := map[string]bool{}
	for _, a := range actions {
		if a.State == "sent" || a.State == "claimed" || a.State == "uncertain" || a.State == "blocked" {
			used[a.TargetSession] = true
		}
	}
	for _, a := range actions {
		if a.State == "done" || a.State == "superseded" {
			continue
		}
		needed, err := c.actionNeeded(b, o, a)
		if err != nil {
			return err
		}
		if !needed {
			if err := c.finishActivation(a, "superseded", "current facts no longer require action"); err != nil {
				return err
			}
			continue
		}
		if a.Kind == "answer" && a.State == "sent" {
			if err := c.finishActivation(a, "done", "answer accepted by its native session"); err != nil {
				return err
			}
			continue
		}
		session := c.carrier(o, a.Session)
		if m, ok := o.Managers[a.Recipient]; ok {
			if !o.Active(m) {
				if err := c.actionAttention(a, "responsible supervisor is no longer active"); err != nil {
					return err
				}
				continue
			}
			session = m.Session
		}
		a, err = c.refreshQuestionPayload(b, o, a, session)
		if err != nil {
			return err
		}
		if _, _, remote := SplitRemote(session); !remote {
			if _, err := c.returnActivationLocal(a, session); err != nil {
				return err
			}
		}
		if a.State == "claimed" && c.now().Sub(a.Dispatched) < time.Minute {
			used[session] = true
			continue
		}
		if a.State == "uncertain" || a.State == "claimed" {
			why := a.Error
			if why == "" {
				why = "previous submission is uncertain; inspect its effects before retrying"
			}
			if err := c.actionAttention(a, why); err != nil {
				return err
			}
			continue
		}
		if handled, err := c.transferActivation(a, session); err != nil {
			return err
		} else if handled {
			continue
		}
		if a.State == "sent" || a.State == "blocked" {
			if a.TargetSession == session {
				used[session] = true
				if a.State == "blocked" {
					if err := c.actionAttention(a, a.Error); err != nil {
						return err
					}
				} else if live, ok := c.reach(b, session); ok && live.Status == Idle && live.ResultAt.After(a.Dispatched) {
					if err := c.actionAttention(a, "supervisor ended its turn without resolving or acknowledging the requested action"); err != nil {
						return err
					}
				}
				continue
			}
			_, err = c.Store.db.Exec(`UPDATE ym_activations SET state='pending' WHERE id=? AND state IN ('sent','blocked')`, a.ID)
			if err != nil {
				return err
			}
		}
		s, ok := c.reach(b, session)
		if !ok || s.Status != Idle || s.Prompt != nil || s.Paused || len(s.Copies) > 1 || used[session] {
			continue
		}
		if err := c.dispatchNativeActions(b, o, s, a, actions); err != nil {
			return err
		}
		used[session] = true
	}
	return nil
}

func (c *Core) CompleteAction(id, session, result string) error {
	if strings.TrimSpace(result) == "" {
		return errors.New("action completion needs a result")
	}
	actions, err := c.Store.Activations()
	if err != nil {
		return err
	}
	for _, a := range actions {
		if a.ID != id {
			continue
		}
		if a.TargetSession == c.Resolve(session) && (a.State == "done" || a.State == "superseded") {
			return nil
		}
		if a.TargetSession != c.Resolve(session) || a.State != "sent" && a.State != "uncertain" && a.State != "claimed" {
			return errors.New("only the accepting supervisor session can complete its current action")
		}
		if a.Kind == "question" {
			imported, err := c.importedActivation(a.ID)
			if err != nil {
				return err
			}
			if !imported {
				needed, err := c.actionNeeded(Board{}, Org{}, a)
				if err != nil {
					return err
				}
				if needed {
					return errors.New("answer or reroute the original question before completing its action")
				}
			}
		}
		return c.finishActivation(a, "done", result)
	}
	return ErrNotFound
}

func (c *Core) RetryAction(id string) error {
	actions, err := c.Store.Activations()
	if err != nil {
		return err
	}
	found := false
	for _, a := range actions {
		if a.ID != id {
			continue
		}
		found = true
		if a.State != "blocked" && a.State != "uncertain" && a.State != "claimed" {
			return errors.New("action is not awaiting recovery")
		}
		b, err := c.Board()
		if err != nil {
			return err
		}
		o, err := c.LoadOrg()
		if err != nil {
			return err
		}
		needed, err := c.actionNeeded(b, o, a)
		if err != nil {
			return err
		}
		if !needed {
			return c.finishActivation(a, "superseded", "current facts no longer require action")
		}
		break
	}
	if !found {
		return ErrNotFound
	}
	if handled, err := c.retryTransferredActivation(id); handled || err != nil {
		return err
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = retryActivationIn(tx, id); err != nil {
		return err
	}
	return tx.Commit()
}

func retryActivationIn(tx *sql.Tx, id string) error {
	var batch, state string
	if err := tx.QueryRow(`SELECT batch,state FROM ym_activations WHERE id=?`, id).Scan(&batch, &state); err != nil {
		return err
	}
	if state != "blocked" && state != "uncertain" && state != "claimed" {
		return errors.New("action is not awaiting recovery")
	}
	_, err := tx.Exec(`UPDATE ym_activations SET state='pending',error='' WHERE (id=? OR (?<>'' AND batch=?)) AND state IN ('blocked','uncertain','claimed')`, id, batch, batch)
	return err
}
