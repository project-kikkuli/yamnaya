package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func answerSubject(a Activation) (int64, int, error) {
	parts := strings.SplitN(a.Subject, ":", 2)
	if len(parts) != 2 {
		return 0, 0, errors.New("answer action needs its item revision")
	}
	seq, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	revision, err := strconv.Atoi(parts[1])
	return seq, revision, err
}

func (c *Core) answerActionNeeded(a Activation) (bool, error) {
	seq, revision, err := answerSubject(a)
	if err != nil {
		return false, err
	}
	it, err := c.Store.Item(fmt.Sprintf("#%d", seq))
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return (it.State == ItemAnswered || it.State == ItemDismissed) && it.Delivered.IsZero() && len(it.History) == revision, nil
}

func settleAnswerIn(tx *sql.Tx, a Activation, at time.Time) error {
	var imported int
	if err := tx.QueryRow(`SELECT count(*) FROM ym_activation_origins WHERE id=? AND origin<>''`, a.ID).Scan(&imported); err != nil {
		return err
	}
	if imported != 0 {
		return nil
	}
	seq, revision, err := answerSubject(a)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE ym_items SET delivered_at=? WHERE seq=? AND delivered_at IS NULL AND state IN ('answered','dismissed') AND (SELECT count(*) FROM ym_item_events WHERE item=?)=?`, stamp(at), seq, fmt.Sprintf("#%d", seq), revision)
	return err
}
