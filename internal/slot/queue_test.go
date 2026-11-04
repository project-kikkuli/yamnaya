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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

const (
	jobPrefix  = "slot job: "
	lowDiskCwd = "/low-disk"
	settle     = 300 * time.Millisecond
	patience   = 20 * time.Second
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
	if low := os.Getenv("SLOT_JOB_LOW_WHILE"); low != "" {
		p.Low = func(extra ...string) (disk.Usage, bool) {
			_, err := os.Stat(low)
			return disk.Usage{Path: "low"}, err == nil && slices.Contains(extra, lowDiskCwd)
		}
	}
	c := Claim{PID: pid, Command: []string{name}, Since: time.Now(), Lane: lane, Cwd: os.Getenv("SLOT_JOB_CWD")}
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
	if command := os.Getenv("SLOT_JOB_COMMAND"); command != "" {
		if _, err := Run([]string{"sh", "-c", command}, 0, nil); err != nil {
			t.Fatal(err)
		}
		return
	}
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
	return q.startCmd(name, lane, exec.Command(os.Args[0], "-test.run=^TestSlotJob$"), env...)
}

func (q *queue) startCmd(name, lane string, cmd *exec.Cmd, env ...string) *job {
	q.t.Helper()
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

func TestPlainWaitersTakeSlotsInArrivalOrder(t *testing.T) {
	q := newQueue(t, 1)
	release := q.occupy(1)
	var line []*job
	for _, n := range rand.Perm(6) {
		name := "job" + strconv.Itoa(n)
		line = append(line, q.start(name, ""))
		q.next("lane " + name)
		q.waiting(name)
	}
	st, err := q.pool.Status()
	if err != nil {
		t.Fatal(err)
	}
	for i, j := range line {
		place := i + 1
		if !listed(st, j.name, func(c Claim) bool { return c.SlotPlace == place }) {
			t.Fatalf("%s is not #%d in %+v", j.name, place, st.Waiters)
		}
	}
	release[0]()
	for _, j := range line {
		q.next("slot " + j.name)
		q.quiet()
		j.finish()
	}
}

func TestADeadPlainWaiterBlocksNoOneBehindIt(t *testing.T) {
	for _, c := range []struct {
		name string
		env  []string
	}{
		{"its pid is gone", nil},
		{"its pid was reused", []string{"SLOT_JOB_PID=" + strconv.Itoa(os.Getpid())}},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueue(t, 1)
			release := q.occupy(1)
			dead := q.start("dead", "", c.env...)
			q.next("lane dead")
			q.waiting("dead")
			q.start("next", "")
			q.next("lane next")
			q.waiting("next")
			dead.kill()
			q.gone("dead")
			release[0]()
			q.next("slot next")
		})
	}
}

func TestALaneHolderBeatsAnOlderPlainWaiterAndTheMemoryHoldStillRules(t *testing.T) {
	for _, c := range []struct {
		name  string
		cause string
	}{
		{"no pressure", ""},
		{"memory hold", "memory"},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueue(t, 2)
			release := q.occupy(2)
			if c.cause != "" {
				if err := (pressure.State{Active: true, Holding: true, Cause: c.cause}).Save(q.pool.Dir); err != nil {
					t.Fatal(err)
				}
			}
			older := q.start("older", "")
			q.next("lane older")
			q.waiting("older")
			suite := q.start("suite", "yamnaya-test")
			q.laneHolder("suite")
			release[0]()
			if c.cause == "" {
				q.next("slot suite")
				release[1]()
				q.next("slot older")
				return
			}
			q.quiet()
			release[1]()
			q.next("slot suite")
			q.quiet()
			suite.finish()
			q.next("slot older")
			older.finish()
		})
	}
}

// oldWaiters race for slots as queued ym processes from before tickets do: any free slot, no line, held briefly.
func (q *queue) oldWaiters() (stop func()) {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			for n := 1; n <= q.pool.Capacity; n++ {
				if f, err := q.pool.tryLock(n); err == nil && f != nil {
					time.Sleep(5 * time.Millisecond)
					f.Close()
				}
			}
			select {
			case <-done:
				return
			case <-time.After(q.pool.Poll):
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}

func TestNewWaitersAreServedInOrderWhileOldWaitersRaceThem(t *testing.T) {
	q := newQueue(t, 1)
	release := q.occupy(1)
	first := q.start("first", "")
	q.next("lane first")
	q.waiting("first")
	second := q.start("second", "")
	q.next("lane second")
	q.waiting("second")
	defer q.oldWaiters()()
	release[0]()
	q.next("slot first")
	first.finish()
	q.next("slot second")
	second.finish()
}

// The real throttle stop path over a real tree: agent stand-in, then sh, then a ym slot waiter at the head of the line. ym's
// throttle spares a process by its kernel name, so a ym keeps its place moving; a build under another name is stopped there.
func TestThrottlingASessionWithAQueuedSlotWaiter(t *testing.T) {
	for _, c := range []struct {
		binary string
		spared bool
	}{
		{"ym", true},
		{"ym.new", false},
		{"ym-new", false},
	} {
		t.Run(c.binary, func(t *testing.T) {
			q := newQueue(t, 1)
			release := q.occupy(1)
			bin := filepath.Join(t.TempDir(), c.binary)
			self, err := os.ReadFile(os.Args[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(bin, self, 0o755); err != nil {
				t.Fatal(err)
			}
			ran := filepath.Join(t.TempDir(), "ran")
			tree := exec.Command("sh", "-c", "sh -c '\"$YM_BIN\" -test.run=^TestSlotJob$; true'; true")
			head := q.startCmd("head", "", tree, "YM_BIN="+bin,
				"SLOT_JOB_COMMAND=i=0; while [ $i -lt 20 ]; do echo $i >> "+ran+"; i=$((i+1)); sleep 0.02; done")
			q.next("lane head")
			q.waiting("head")
			q.start("other", "")
			q.next("lane other")
			q.waiting("other")

			var throttle pressure.Throttle
			list := func() ([]pressure.Process, error) { return pressure.Processes(context.Background()) }
			if _, err := throttle.Pause(time.Now(), list, map[int]string{head.cmd.Process.Pid: "agent"}, pressure.Signal); err != nil {
				t.Fatal(err)
			}
			resume := func() {
				if _, err := throttle.Resume(time.Now(), func(string) bool { return true }, pressure.Signal); err != nil {
					t.Error(err)
				}
			}
			defer resume()
			stopped := map[string]bool{}
			for _, p := range throttle.Paused {
				stopped[filepath.Base(p.Command)] = true
			}
			if !stopped["sh"] || stopped[c.binary] == c.spared {
				t.Fatalf("stopped %v, want sh, and %s spared %v", stopped, c.binary, c.spared)
			}

			release[0]()
			if !c.spared {
				q.quiet()
				resume()
				q.next("slot head")
				return
			}
			// the paused session's job takes its turn, runs to its end with its agent stopped, and passes the slot on
			q.next("slot head")
			q.next("slot other")
			lines, err := os.ReadFile(ran)
			if err != nil || strings.Count(string(lines), "\n") != 20 {
				t.Fatalf("the job's output %q, %v, want all 20 lines", lines, err)
			}
		})
	}
}

func TestAJobItsOwnDiskHoldsBackHoldsUpNobodyAndKeepsItsPlace(t *testing.T) {
	for _, c := range []struct {
		name string
		lane string
	}{
		{"the head of the plain line", ""},
		{"a lane holder waiting for a slot", "yamnaya-test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := newQueue(t, 1)
			release := q.occupy(1)
			low := filepath.Join(t.TempDir(), "low")
			if err := os.WriteFile(low, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			env := []string{"SLOT_JOB_LOW_WHILE=" + low}
			q.start("held", c.lane, append(env, "SLOT_JOB_CWD="+lowDiskCwd)...)
			q.next("lane held")
			q.waiting("held")
			other := q.start("other", "", env...)
			q.next("lane other")
			q.waiting("other")
			release[0]()
			q.next("slot other")
			q.quiet()
			if err := os.Remove(low); err != nil {
				t.Fatal(err)
			}
			third := q.start("third", "", env...)
			q.next("lane third")
			q.waiting("third")
			other.finish()
			q.next("slot held")
			q.quiet()
			third.kill()
		})
	}
}
