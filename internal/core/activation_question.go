package core

import "strings"

func (c *Core) refreshQuestionPayload(b Board, o Org, a Activation, session string) (Activation, error) {
	if a.Kind != "question" {
		return a, nil
	}
	imported, err := c.importedActivation(a.ID)
	if err != nil || imported {
		return a, err
	}
	it, err := c.Store.Item(strings.SplitN(a.Subject, "@", 2)[0])
	if err != nil {
		return a, err
	}
	if _, _, remote := SplitRemote(session); remote {
		it.Host = c.SelfName()
	}
	payload := c.Store.Nicked(askText(b, o, it))
	if payload != a.Payload {
		if _, err = c.Store.db.Exec(`UPDATE ym_activations SET payload=? WHERE id=? AND payload<>?`, payload, a.ID, payload); err != nil {
			return a, err
		}
		a.Payload = payload
	}
	return a, nil
}
