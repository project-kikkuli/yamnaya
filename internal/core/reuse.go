package core

import (
	"reflect"
	"slices"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// Changing is a harness that counts every change it read in its files, so a board can tell that nothing it shows moved.
type Changing interface {
	Generation() uint64
}

// inputs is everything a board is built from, as cheaply as each can be told: the processes on terminals, each harness's
// count of file changes, the database's data_version, every directory's place in git, launches and the remote hosts' boards.
type inputs struct {
	procs    uint64
	nprocs   int
	harness  []uint64
	data     int64
	git      uint64
	launched []Launched
	dormant  []Dormant
	remotes  []time.Time
}

// boardRecheck is how long a board built from unchanged inputs stands before it is built again, for what changes with time
// alone, such as a quiet goal crossing its threshold.
const boardRecheck = 10 * time.Second

type boardReuse struct {
	mu    sync.Mutex
	gen   uint64
	last  inputs
	board Board
	at    time.Time
	ok    bool
}

// fingerprint reads the board's inputs; reusable is false when one of them cannot be told, such as a harness that counts no
// changes but runs sessions.
func (c *Core) fingerprint(procs uint64, n int, owned [][]proc.Proc) (inputs, bool) {
	in := inputs{procs: procs, nprocs: n}
	reusable := true
	for i, h := range c.Harnesses {
		if ch, ok := h.(Changing); ok {
			in.harness = append(in.harness, ch.Generation())
		} else if len(owned[i]) > 0 {
			reusable = false
		}
	}
	var ok bool
	c.Store.memo.mu.Lock()
	in.data, ok = c.Store.memo.current(c.Store.db)
	c.Store.memo.mu.Unlock()
	reusable = reusable && ok
	c.gc.mu.Lock()
	in.git = c.gc.gen
	c.gc.mu.Unlock()
	if c.Launches != nil {
		in.launched, in.dormant = c.Launches.Launched(), c.Launches.Dormant()
	}
	if c.ShowRemotes {
		for _, r := range c.remoteBoards() {
			in.remotes = append(in.remotes, r.At)
		}
	}
	return in, reusable
}

func (r *boardReuse) get(in inputs, reusable bool, now time.Time) (Board, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !reusable || !r.ok || now.Before(r.at) || now.Sub(r.at) >= boardRecheck || !reflect.DeepEqual(in, r.last) {
		return Board{}, false
	}
	return r.board, true
}

func (r *boardReuse) put(in inputs, reusable bool, b *Board) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	b.Gen = r.gen
	r.last, r.board, r.at, r.ok = in, *b, b.At, reusable
}

// renumber gives a reused board whose spend changed a number of its own, and keeps it as the board to reuse.
func (r *boardReuse) renumber(b *Board) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	b.Gen = r.gen
	at := r.board.At
	r.board = *b
	r.board.At = at
}

// again is a reused board at now, with spend read afresh.
func (c *Core) again(b Board, now time.Time) Board {
	b.At = now
	if c.Meter != nil {
		fleet := MergeFleet(c.Meter.Fleet(now), b.Remotes)
		if reflect.DeepEqual(fleet, b.Fleet) {
			return b
		}
		b.Fleet = fleet
		b.Projects = slices.Clone(b.Projects)
		for i := range b.Projects {
			b.Projects[i].Spend = projectSpend(b.Projects[i], b.Fleet)
		}
		if err := c.settleStats(&b); err != nil && c.Log != nil {
			c.Log("board stats: " + err.Error())
		}
		b.Repos = repos(b.Projects)
		b.indexSessions()
		c.reuse.renumber(&b)
	}
	return b
}
