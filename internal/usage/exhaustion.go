package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func exhaustionEvent(line []byte, harness, model string) *core.Exhaustion {
	var doc struct {
		Type      string    `json:"type"`
		Timestamp time.Time `json:"timestamp"`
		IsAPI     bool      `json:"isApiErrorMessage"`
		Error     string    `json:"error"`
		Message   struct {
			Model string `json:"model"`
		} `json:"message"`
		Payload struct {
			Type    string `json:"type"`
			Info    string `json:"codex_error_info"`
			Message string `json:"message"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &doc) != nil {
		return nil
	}
	reason := ""
	if harness == codexName && doc.Type == "event_msg" && doc.Payload.Type == "error" && (doc.Payload.Info == "UsageLimitExceeded" || doc.Payload.Info == "usage_limit_exceeded") {
		reason = "usage_limit_exceeded"
	}
	if harness == claudeName && doc.Type == "assistant" && doc.IsAPI && doc.Error == "rate_limit" {
		reason = "subscription_rate_limit"
		if doc.Message.Model != "<synthetic>" {
			model = doc.Message.Model
		}
	}
	if reason == "" {
		return nil
	}
	hash := sha256.Sum256(line)
	return &core.Exhaustion{Harness: harness, Model: model, Reason: reason, Seen: doc.Timestamp, Event: hex.EncodeToString(hash[:])}
}
func exhaustionKey(e core.Exhaustion) string { return e.Harness + "/" + e.Profile + "/" + e.Session }
func (m *Meter) saveExhaustions(work []*parsed) error {
	for _, p := range work {
		events := p.exhaustions
		for _, r := range append(p.claude.responses, p.codex.turns...) {
			events = append(events, core.Exhaustion{Seen: time.Unix(r.At, 0), Recovered: true, Event: r.ID})
		}
		for _, e := range events {
			e.Harness = p.f.Harness
			e.Profile = p.f.Profile
			e.Account = p.f.Account
			e.Session = p.f.Session
			key := exhaustionKey(e)
			old, ok := m.exhaustions[key]
			if ok && old.Seen.After(e.Seen) {
				continue
			}
			if ok && old.Seen.Equal(e.Seen) && !old.Recovered && e.Recovered {
				continue
			}
			body, err := json.Marshal(e)
			if err != nil {
				return err
			}
			if _, err = m.ledger.db.Exec(`INSERT INTO ym_usage_exhaustions(key,body) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET body=excluded.body`, key, string(body)); err != nil {
				return err
			}
			m.exhaustions[key] = e
		}
	}
	return nil
}
