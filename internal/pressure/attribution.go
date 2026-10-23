package pressure

import (
	"path/filepath"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

type Identity struct{ Thread, Cwd string }

func AppServers(procs []proc.Proc) map[int]bool {
	out := map[int]bool{}
	for _, p := range procs {
		if p.Command() != "codex" {
			continue
		}
		for _, arg := range p.Params() {
			if arg == "app-server" {
				out[p.PID] = true
				break
			}
		}
	}
	return out
}

// AppJobs attributes tool processes beneath shared app servers without claiming the server or other sessions.
func AppJobs(procs []Process, servers map[int]bool, sessions, worktrees map[string]Owner, read func(int) Identity) map[int]Owner {
	table := map[int]Process{}
	for _, p := range procs {
		table[p.PID] = p
	}
	identities := map[int]Identity{}
	identity := func(pid int) Identity {
		if id, ok := identities[pid]; ok {
			return id
		}
		id := read(pid)
		identities[pid] = id
		return id
	}
	jobs := map[int]Owner{}
	for _, p := range procs {
		if p.CPU <= 0 || servers[p.PID] || protected(p.Command) {
			continue
		}
		var chain []int
		seen := map[int]bool{}
		server := false
		for pid := p.PID; pid > 1 && !seen[pid]; pid = table[pid].PPID {
			seen[pid] = true
			if servers[pid] {
				server = true
				break
			}
			chain = append(chain, pid)
		}
		if !server {
			continue
		}
		for _, pid := range chain {
			id := identity(pid)
			if owner, ok := sessions[id.Thread]; ok {
				jobs[p.PID] = owner
				break
			}
			best := ""
			for dir := range worktrees {
				dir = filepath.Clean(dir)
				cwd := filepath.Clean(id.Cwd)
				if (cwd == dir || strings.HasPrefix(cwd, dir+string(filepath.Separator))) && len(dir) > len(best) {
					best = dir
				}
			}
			if best != "" {
				jobs[p.PID] = worktrees[best]
				break
			}
		}
	}
	return jobs
}
