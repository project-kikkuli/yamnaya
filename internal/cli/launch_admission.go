package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func machineLaunchAdmission(stateDir string) func(core.NewLaunch) error {
	var mu sync.Mutex
	var meter pressure.Meter
	return func(n core.NewLaunch) error {
		mu.Lock()
		defer mu.Unlock()
		host, err := meter.Read()
		if err != nil {
			return &core.LaunchDeferred{Reason: "cannot read machine capacity: " + err.Error()}
		}
		pool, err := slot.Machine()
		if err != nil {
			return &core.LaunchDeferred{Reason: "cannot read machine capacity: " + err.Error()}
		}
		load, err := slot.Load()
		if err != nil {
			return &core.LaunchDeferred{Reason: "cannot read processor load: " + err.Error()}
		}
		throttle, err := pressure.ReadThrottle(pool.Dir)
		if err != nil {
			return &core.LaunchDeferred{Reason: "cannot read machine throttle: " + err.Error()}
		}
		if throttle.On() {
			return &core.LaunchDeferred{Reason: "machine throttle is on"}
		}
		if host.Level >= pressure.LevelWarn {
			return &core.LaunchDeferred{Reason: fmt.Sprintf("memory pressure level %d, swap %.1f GiB", host.Level, float64(host.Swap)/(1<<30))}
		}
		if pool.Cores > 0 && load >= float64(pool.Cores) {
			return &core.LaunchDeferred{Reason: fmt.Sprintf("processor load %.1f across %d cores", load, pool.Cores)}
		}
		path := n.Repo
		if path == "" || !core.IsRepoPath(path) {
			path = stateDir
		}
		usage, err := disk.Stat(path)
		for errors.Is(err, os.ErrNotExist) && filepath.Dir(path) != path {
			path = filepath.Dir(path)
			usage, err = disk.Stat(path)
		}
		if err != nil {
			return &core.LaunchDeferred{Reason: "cannot read launch filesystem: " + err.Error()}
		}
		if usage.Free() < disk.Tier2Free {
			return &core.LaunchDeferred{Reason: fmt.Sprintf("filesystem %.0f%% free", usage.Free()*100)}
		}
		return nil
	}
}
