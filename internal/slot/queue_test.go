package slot

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

const (
	jobPrefix = "slot job: "
	settle    = 300 * time.Millisecond
	patience  = 20 * time.Second
)

// TestSlotJob is one real ym slot queue member run by the tests below: it takes its lane and a slot, says so, and holds
// them until its stdin closes.
func TestSlotJob(t *testing.T) {
	dir := os.Getenv("SLOT_JOB_DIR")
	if dir == "" {
		t.Skip("run by the queue tests")
	}
	name, lane := os.Getenv("SLOT_JOB_NAME"), os.Getenv("SLOT_JOB_LANE")
	capacity, _ := strconv.Atoi(os.Getenv("SLOT_JOB_CAPACITY"))
	pid := os.Getpid()
	if claimed, err := strconv.Atoi(os.Getenv("SLOT_JOB_PID")); err == nil {
		pid = claimed
	}
	p := Pool{Dir: dir, Capacity: capacity, Cores: 4, Poll: 10 * time.Millisecond}
	c := Claim{PID: pid, Command: []string{name}, Since: time.Now(), Lane: lane}
	leave, seated, err := p.Lane(context.Background(), lane, c, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer leave()
	fmt.Println(jobPrefix + "lane " + name)
	release, _, err := p.Acquire(context.Background(), c, nil)
	seated()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	fmt.Println(jobPrefix + "slot " + name)
	io.Copy(io.Discard, os.Stdin)
}

type queue struct {
	t      *testing.T
	pool   Pool
	events chan string
}

type job struct {
	name  string
	cmd   *exec.Cmd
	stdin io.Closer
}

func newQueue(t *testing.T, capacity int) *queue {
	p := pool(t)
	p.Capacity = capacity
	return &queue{t: t, pool: p, events: make(chan string, 64)}
}

func (q *queue) start(name, lane string, env ...string) *job {
	q.t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSlotJob$")
	cmd.Env = append(os.Environ(), "SLOT_JOB_DIR="+q.pool.Dir, "SLOT_JOB_NAME="+name, "SLOT_JOB_LANE="+lane, "SLOT_JOB_CAPACITY="+strconv.Itoa(q.pool.Capacity))
	cmd.Env = append(cmd.Env, env...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		q.t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		q.t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		q.t.Fatal(err)
	}
	go func() {
		lines := bufio.NewScanner(stdout)
		for lines.Scan() {
			if event, ok := strings.CutPrefix(lines.Text(), jobPrefix); ok {
				q.events <- event
			}
		}
	}()
	j := &job{name: name, cmd: cmd, stdin: stdin}
	q.t.Cleanup(func() { j.kill() })
	return j
}

// kill ends j as SIGKILL does, with no chance to clean up.
func (j *job) kill() {
	j.cmd.Process.Kill()
	j.cmd.Wait()
}

// finish lets j release its slot and lane and exit.
func (j *job) finish() {
	j.stdin.Close()
	j.cmd.Wait()
}

func (q *queue) next(want ...string) string {
	q.t.Helper()
	select {
	case got := <-q.events:
		for _, w := range want {
			if got == w {
				return got
			}
		}
		q.t.Fatalf("event %q, want one of %q", got, want)
	case <-time.After(patience):
		q.t.Fatalf("no event, want one of %q", want)
	}
	return ""
}

func (q *queue) quiet() {
	q.t.Helper()
	select {
	case got := <-q.events:
		q.t.Fatalf("unexpected event %q", got)
	case <-time.After(settle):
	}
}

func (q *queue) waitFor(what string, ok func(Status) bool) {
	q.t.Helper()
	for deadline := time.Now().Add(patience); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		st, err := q.pool.Status()
		if err != nil {
			q.t.Fatal(err)
		}
		if ok(st) {
			return
		}
	}
	st, _ := q.pool.Status()
	q.t.Fatalf("never saw %s in %+v", what, st)
}

func listed(st Status, name string, match func(Claim) bool) bool {
	for _, c := range st.Waiters {
		if c.Line() == name && match(c) {
			return true
		}
	}
	return false
}

func (q *queue) waiting(name string) {
	q.t.Helper()
	q.waitFor(name+" waiting", func(st Status) bool { return listed(st, name, func(Claim) bool { return true }) })
}

// laneHolder waits for name to be its lane's holder, waiting for a slot.
func (q *queue) laneHolder(name string) {
	q.t.Helper()
	q.next("lane " + name)
	q.waitFor(name+" holding its lane", func(st Status) bool { return listed(st, name, func(c Claim) bool { return c.LaneHolder }) })
}

func (q *queue) gone(name string) {
	q.t.Helper()
	q.waitFor(name+" gone", func(st Status) bool { return !listed(st, name, func(Claim) bool { return true }) })
}

// occupy holds n slots the way agent-slot's lock does, with no claim, and returns one release per slot.
func (q *queue) occupy(n int) []func() {
	q.t.Helper()
	if err := os.MkdirAll(q.pool.Dir, 0o755); err != nil {
		q.t.Fatal(err)
	}
	var releases []func()
	for slot := 1; slot <= n; slot++ {
		f, err := q.pool.tryLock(slot)
		if err != nil || f == nil {
			q.t.Fatalf("slot %d: %v", slot, err)
		}
		q.t.Cleanup(func() { f.Close() })
		releases = append(releases, func() { f.Close() })
	}
	return releases
}

// oldHolder takes lane name's flock as a ym from before tickets does and returns its release.
func (q *queue) oldHolder(name string) func() {
	q.t.Helper()
	path := filepath.Join(q.pool.Dir, fmt.Sprintf("lane-%x.lock", sha256.Sum256([]byte(name))))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		q.t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		q.t.Fatal(err)
	}
	q.t.Cleanup(func() { f.Close() })
	return func() { f.Close() }
}

func TestALaneHolderTakesAFreedSlotAheadOfOtherWaiters(t *testing.T) {
	for _, c := range []struct {
		name      string
		cause     string
		laneFirst bool
		capacity  int
	}{
		{name: "no pressure, lane job arrives last", capacity: 1},
		{name: "no pressure, lane job arrives first", laneFirst: true, capacity: 1},
		{name: "cpu strain", cause: "cpu", capacity: 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueue(t, c.capacity)
			release := q.occupy(c.capacity)
			if c.cause != "" {
				if err := (pressure.State{Active: true, Holding: true, Cause: c.cause}).Save(q.pool.Dir); err != nil {
					t.Fatal(err)
				}
			}
			if c.laneFirst {
				q.start("suite", "yamnaya-test")
				q.laneHolder("suite")
			}
			for i := range 4 {
				name := "other" + strconv.Itoa(i)
				q.start(name, "")
				q.waiting(name)
				q.next("lane " + name)
			}
			if !c.laneFirst {
				q.start("suite", "yamnaya-test")
				q.laneHolder("suite")
			}
			release[0]()
			q.next("slot suite")
			q.quiet()
		})
	}
}

func TestALaneHolderStillWaitsOutTheMemoryHold(t *testing.T) {
	q := newQueue(t, 2)
	release := q.occupy(2)
	if err := (pressure.State{Active: true, Holding: true, Cause: "memory"}).Save(q.pool.Dir); err != nil {
		t.Fatal(err)
	}
	other := q.start("other", "")
	q.waiting("other")
	q.next("lane other")
	suite := q.start("suite", "yamnaya-test")
	q.laneHolder("suite")

	release[0]()
	q.quiet()
	release[1]()
	q.next("slot suite")
	q.quiet()
	suite.finish()
	q.next("slot other")
	other.finish()
}

func TestLaneWaitersTakeTheLaneInArrivalOrder(t *testing.T) {
	for _, c := range []struct {
		name string
		hold func(q *queue) func()
	}{
		{"behind a job from a ym older than tickets", func(q *queue) func() { return q.oldHolder("yamnaya-test") }},
		{"behind a job holding a ticket", func(q *queue) func() {
			first := q.start("first", "yamnaya-test")
			q.next("lane first")
			q.next("slot first")
			return first.finish
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueue(t, 8)
			if err := os.MkdirAll(q.pool.Dir, 0o755); err != nil {
				t.Fatal(err)
			}
			free := c.hold(q)
			var line []*job
			for _, n := range rand.Perm(6) {
				name := "job" + strconv.Itoa(n)
				line = append(line, q.start(name, "yamnaya-test"))
				q.waiting(name)
			}
			q.quiet()
			st, err := q.pool.Status()
			if err != nil {
				t.Fatal(err)
			}
			for i, j := range line {
				place := i + 1
				if !listed(st, j.name, func(c Claim) bool { return c.LanePlace == place && !c.LaneHolder }) {
					t.Fatalf("%s is not #%d in %+v", j.name, place, st.Waiters)
				}
			}
			free()
			for _, j := range line {
				q.next("lane " + j.name)
				q.next("slot " + j.name)
				j.finish()
			}
		})
	}
}

func TestDeadLaneJobsBlockNobody(t *testing.T) {
	// a pid that stays alive throughout stands in for a dead job's pid being reused
	reused := "SLOT_JOB_PID=" + strconv.Itoa(os.Getpid())
	for _, c := range []struct {
		name string
		env  []string
	}{
		{"its pid is gone", nil},
		{"its pid was reused", []string{reused}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Run("head of the line", func(t *testing.T) {
				q := newQueue(t, 8)
				release := q.oldHolder("yamnaya-test")
				dead := q.start("dead", "yamnaya-test", c.env...)
				q.waiting("dead")
				q.start("next", "yamnaya-test")
				q.waiting("next")
				dead.kill()
				q.gone("dead")
				release()
				q.next("lane next")
				q.next("slot next")
			})
			t.Run("middle of the line", func(t *testing.T) {
				q := newQueue(t, 8)
				release := q.oldHolder("yamnaya-test")
				q.start("first", "yamnaya-test")
				q.waiting("first")
				dead := q.start("dead", "yamnaya-test", c.env...)
				q.waiting("dead")
				last := "last"
				q.start(last, "yamnaya-test")
				q.waiting(last)
				dead.kill()
				q.waitFor("last moving up", func(st Status) bool {
					return listed(st, last, func(c Claim) bool { return c.LanePlace == 2 })
				})
				release()
				q.next("lane first")
				q.next("slot first")
			})
			t.Run("lane holder waiting for a slot, none behind it", func(t *testing.T) {
				q := newQueue(t, 1)
				release := q.occupy(1)
				dead := q.start("dead", "yamnaya-test", c.env...)
				q.laneHolder("dead")
				q.start("other", "")
				q.waiting("other")
				q.next("lane other")
				dead.kill()
				q.gone("dead")
				release[0]()
				q.next("slot other")
			})
			t.Run("lane holder waiting for a slot, next in line behind it", func(t *testing.T) {
				q := newQueue(t, 1)
				release := q.occupy(1)
				dead := q.start("dead", "yamnaya-test", c.env...)
				q.laneHolder("dead")
				q.start("next", "yamnaya-test")
				q.waiting("next")
				q.start("other", "")
				q.waiting("other")
				q.next("lane other")
				dead.kill()
				q.laneHolder("next")
				release[0]()
				q.next("slot next")
			})
		})
	}
}

func TestEachLaneHasOneHolderAheadOfOtherWaiters(t *testing.T) {
	q := newQueue(t, 1)
	release := q.occupy(1)
	q.start("other", "")
	q.waiting("other")
	q.next("lane other")
	a := q.start("a1", "lane-a")
	q.laneHolder("a1")
	b := q.start("b1", "lane-b")
	q.laneHolder("b1")
	q.start("a2", "lane-a")
	q.waiting("a2")
	release[0]()
	first := q.next("slot a1", "slot b1")
	q.quiet()
	if first == "slot b1" {
		b.finish()
		q.next("slot a1")
		q.quiet()
		return
	}
	// a1 leaving makes a2 lane-a's holder, which takes its turn among the holders; other still waits for them all
	a.finish()
	want := []string{"lane a2", "slot b1", "slot a2"}
	q.next(want...)
	q.next(want...)
}

func TestAgentSlotLocksNeedNoQueueAndBlockNoLane(t *testing.T) {
	q := newQueue(t, 2)
	release := q.occupy(1)
	q.start("suite", "yamnaya-test")
	q.next("lane suite")
	q.next("slot suite")
	st, err := q.pool.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Holders) != 2 || st.Holders[0].Line() != "agent-slot (no details)" || !st.Holders[1].LaneHolder {
		t.Fatalf("holders %+v", st.Holders)
	}
	release[0]()

	waiting := newQueue(t, 1)
	held := waiting.occupy(1)
	waiting.start("suite", "yamnaya-test")
	waiting.laneHolder("suite")
	held[0]()
	waiting.next("slot suite")
}

func TestAJobOutOfTheLaneIsListedAsItsHolderOnceSeated(t *testing.T) {
	q := newQueue(t, 2)
	q.start("suite", "yamnaya-test")
	q.next("lane suite")
	q.next("slot suite")
	q.waitFor("no lane holder waiting", func(st Status) bool {
		return len(st.Waiters) == 0 && len(st.Holders) == 1 && st.Holders[0].LaneHolder && st.Holders[0].Lane == "yamnaya-test"
	})
	other := q.start("other", "")
	q.next("lane other")
	q.next("slot other")
	other.finish()
}

func TestStatusLeavesAWaiterWritingItsClaimAlone(t *testing.T) {
	p := pool(t)
	release, _, err := p.Acquire(context.Background(), Claim{PID: os.Getpid()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				p.Status()
			}
		}
	}()
	for range 2000 {
		ctx, cancel := context.WithCancel(context.Background())
		if _, _, err := p.Acquire(ctx, Claim{PID: os.Getpid()}, cancel); err != context.Canceled {
			t.Fatalf("a waiter failed while status read its claim: %v", err)
		}
	}
}
