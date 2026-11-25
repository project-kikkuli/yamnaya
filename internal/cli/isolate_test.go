package cli

import (
	"bytes"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// isolatedEnv names the one test a copy of the test binary runs itself; TestMain moves it into isolated, so nothing that copy starts inherits it.
const isolatedEnv = "YM_CLI_TEST_ISOLATED"

var isolated string

// isolatedAtOnce is how many isolated copies run at once unless -parallel says: each mostly waits on the processes it starts.
const isolatedAtOnce = 8

// isolate runs body in a copy of the test binary with a test root and tmux servers of its own, so tests that set the process's
// environment run alongside each other.
func isolate(t *testing.T, body func(*testing.T)) {
	t.Helper()
	if isolated == t.Name() {
		body(t)
		return
	}
	t.Parallel()
	parts := strings.Split(t.Name(), "/")
	for i, p := range parts {
		parts[i] = "^" + regexp.QuoteMeta(p) + "$"
	}
	cmd := exec.Command(os.Args[0], "-test.run="+strings.Join(parts, "/"), "-test.count=1", "-test.v")
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "YM_AGENTTEST_ROOT=") })
	cmd.Env = append(env, isolatedEnv+"="+t.Name())
	out, err := cmd.CombinedOutput()
	if err != nil || !bytes.Contains(out, []byte("--- PASS: "+t.Name()+" (")) {
		t.Fatalf("%s in a copy of its own: %v\n%s", t.Name(), err, out)
	}
}
