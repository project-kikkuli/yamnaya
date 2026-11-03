package cli

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

func (l launcher) switchReady(s core.Live, rec launch.Record, fleet *core.Fleet) error {
	if err := switchIdentity(s, rec); err != nil {
		return err
	}
	return l.turnEnded(s, fleet)
}

func switchIdentity(s core.Live, rec launch.Record) error {
	for _, cp := range append([]core.Session{s.Session}, s.Copies...) {
		if cp.Harness != s.Harness || cp.ID != s.ID || profile.ID(cp.Profile) != profile.ID(s.Profile) || cp.Account != s.Account || physicalProfileRoot(cp.ConfigRoot) != physicalProfileRoot(s.ConfigRoot) {
			return fmt.Errorf("session copies have inconsistent source identities")
		}
		if rec.Session != "" && (rec.Session != cp.ID || rec.Harness != cp.Harness || profile.ID(rec.Profile) != profile.ID(cp.Profile) || rec.ConfigRoot != "" && physicalProfileRoot(rec.ConfigRoot) != physicalProfileRoot(cp.ConfigRoot) || rec.Account != "" && rec.Account != cp.Account) {
			return fmt.Errorf("native source identity changed; reopen its login panel")
		}
	}
	return nil
}

// turnEnded fails while s is mid-turn, shows a dialog, or waits on anything but a verified subscription exhaustion; busy crew never block it.
func (l launcher) turnEnded(s core.Live, fleet *core.Fleet) error {
	for _, cp := range append([]core.Session{s.Session}, s.Copies...) {
		if cp.Prompt != nil {
			return fmt.Errorf("a native prompt is active; source is unchanged")
		}
		if cp.Status == core.Idle {
			continue
		}
		if cp.Status != core.Waiting || l.core == nil {
			return fmt.Errorf("%s is %s or has a live action", core.Short(cp.ID), cp.Status)
		}
		mainAt, err := l.core.InspectHandoff(cp)
		if err != nil {
			return fmt.Errorf("cannot switch waiting session: %w", err)
		}
		if fleet == nil {
			dir := filepath.Dir(l.records.Dir)
			events, err := usage.KnownExhaustions(dir)
			if err != nil {
				return fmt.Errorf("cannot verify subscription exhaustion: %w", err)
			}
			fleet = &core.Fleet{At: time.Now(), Exhaustions: events, Limits: profileLimits(dir)}
		}
		now := time.Now()
		verified := false
		for _, e := range fleet.Exhaustions {
			if e.Harness != cp.Harness || e.Session != cp.ID || profile.ID(e.Profile) != profile.ID(cp.Profile) || e.Account == "" || e.Account != cp.Account || e.Seen.Before(mainAt) || e.Seen.After(now) || mainAt.After(now) {
				continue
			}
			_, verified = trueExhaustion(e, fleet.Limits, now)
			if verified {
				break
			}
		}
		if !verified {
			return fmt.Errorf("waiting session has no current matching subscription exhaustion")
		}
	}
	return nil
}
