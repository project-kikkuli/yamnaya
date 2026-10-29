package mcp

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type Observation struct {
	Auth   string    `json:"auth"`
	Health string    `json:"health"`
	At     time.Time `json:"at"`
}

func observationPath(state string, t Target, s Server) string {
	key := t.Host + "\x00" + t.Harness + "\x00" + t.Profile + "\x00" + t.Native.Account + "\x00" + t.Root + "\x00" + t.ConfigFile + "\x00" + t.Cwd + "\x00" + s.Name + "\x00" + s.Scope + "\x00" + s.Source + "\x00" + s.Context + "\x00" + s.Fingerprint + "\x00" + s.Approval + "\x00" + s.Configuration + fmt.Sprint(s.Effective)
	return filepath.Join(state, "mcp-status", fmt.Sprintf("%x.json", sha256.Sum256([]byte(key))))
}

func SaveObservation(state string, t Target, s Server, o Observation) error {
	b, err := json.Marshal(o)
	if err != nil {
		return err
	}
	return profile.Write(observationPath(state, t, s), b)
}

func ForgetObservation(state string, t Target, s Server) error {
	err := os.Remove(observationPath(state, t, s))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func (i *Inventory) Observations(state string, now time.Time) {
	for n := range i.Servers {
		s := &i.Servers[n]
		if !s.Effective || s.Configuration != "configured" {
			continue
		}
		b, err := os.ReadFile(observationPath(state, i.Target, *s))
		if err != nil {
			continue
		}
		var o Observation
		if json.Unmarshal(b, &o) != nil {
			continue
		}
		s.Auth, s.Health, s.CheckedAt = o.Auth, o.Health, o.At
		if now.Sub(o.At) > 15*time.Minute {
			s.Health = "stale"
			s.Auth = "unknown"
		}
	}
}
