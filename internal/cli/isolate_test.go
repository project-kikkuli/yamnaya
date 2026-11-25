package cli

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// isolatedEnv names the one test a copy of the test binary runs itself; TestMain moves it into isolated, so nothing that copy starts inherits it.
const isolatedEnv = "YM_CLI_TEST_ISOLATED"

var isolated string

// endWithParent kills this copy's process group, itself included, once the parent holding the other end of fd 3 is gone, however
// it died; the copy's watcher, in a session of its own, then clears its tmux servers and root.
func endWithParent() {
	parent := os.NewFile(3, "parent")
	go func() {
		io.Copy(io.Discard, parent)
		syscall.Kill(-os.Getpid(), syscall.SIGKILL)
	}()
}

// isolatedAtOnce is how many isolated copies run at once unless -parallel says: each mostly waits on the processes it starts.
const isolatedAtOnce = 8

// isolateGrace is how long before the test deadline a copy times out by itself, and as long again before it is killed; a
// copy with under three graces left splits what it has into thirds.
const isolateGrace = 5 * time.Second

// isolate runs body in a copy of the test binary with a test root and tmux servers of its own, so tests that set the process's
// environment run alongside each other.
func isolate(t *testing.T, body func(*testing.T)) {
	t.Helper()
	if isolated == t.Name() {
		body(t)
		return
	}
	t.Parallel()
	args := []string{"-test.run=" + isolatedRun(t.Name(), flag.Lookup("test.run").Value.String()), "-test.count=1", "-test.v"}
	flag.Visit(func(f *flag.Flag) {
		if slices.Contains([]string{"test.parallel", "test.failfast", "test.short", "test.skip"}, f.Name) {
			args = append(args, "-"+f.Name+"="+f.Value.String())
		}
	})
	ctx, grace := context.Background(), isolateGrace
	if deadline, ok := t.Deadline(); ok {
		left := time.Until(deadline)
		grace = max(min(isolateGrace, left/3), 0)
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, deadline.Add(-grace))
		defer cancel()
		args = append(args, "-test.timeout="+max(left-2*grace, time.Millisecond).String())
	} else {
		args = append(args, "-test.timeout=0")
	}
	cmd := exec.CommandContext(ctx, os.Args[0], args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = grace
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "YM_AGENTTEST_ROOT=") })
	cmd.Env = append(env, isolatedEnv+"="+t.Name())
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	parent, held, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	cmd.ExtraFiles = []*os.File{parent}
	err = cmd.Start()
	parent.Close()
	if err != nil {
		t.Fatalf("%s in a copy of its own did not start: %v", t.Name(), err)
	}
	err = cmd.Wait()
	if syscall.Kill(-cmd.Process.Pid, 0) == nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	if err != nil || !bytes.Contains(out.Bytes(), []byte("--- PASS: "+t.Name()+" (")) {
		t.Fatalf("%s in a copy of its own: %v\n%s", t.Name(), err, out.Bytes())
	}
}

// isolatedRun is the -run pattern a copy runs test with: test alone at the top, and whatever each alternative of run naming it asks of its
// subtests, split as the testing package splits it.
func isolatedRun(test, run string) string {
	top := "^" + regexp.QuoteMeta(test) + "$"
	var alts []string
	for _, alt := range splitRun(run, '|') {
		levels := splitRun(alt, '/')
		if ok, err := regexp.MatchString(levels[0], test); err != nil || !ok {
			continue
		}
		if len(levels) == 1 {
			return top
		}
		alts = append(alts, top+"/"+strings.Join(levels[1:], "/"))
	}
	if len(alts) == 0 {
		return top
	}
	return strings.Join(alts, "|")
}

// splitRun splits s at each sep outside brackets and parentheses.
func splitRun(s string, sep byte) []string {
	var parts []string
	brackets, parens, start := 0, 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			brackets++
		case ']':
			brackets = max(brackets-1, 0)
		case '(':
			if brackets == 0 {
				parens++
			}
		case ')':
			if brackets == 0 {
				parens--
			}
		case '\\':
			i++
		case sep:
			if brackets == 0 && parens == 0 {
				parts = append(parts, s[start:i])
				start = i + 1
			}
		}
	}
	return append(parts, s[start:])
}
