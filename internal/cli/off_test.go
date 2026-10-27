package cli

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestCommandsFromASessionYmIsOffForRefuseUnlessTheyOnlyRead(t *testing.T) {
	const self = "0ff0ff00-0000-4000-8000-000000000001"
	for _, c := range []struct {
		args   []string
		refuse bool
		// launches marks a command that would start or type into a real session once it passes, so only its refusal runs.
		launches bool
	}{
		{[]string{"item", "add", "--question", "q?", "--kind", "decision", "--rec", "r", "--option", "a=x", "--option", "b=y"}, true, false},
		{[]string{"item", "inbox"}, true, false},
		{[]string{"goal", "update", "moved"}, true, false},
		{[]string{"goal", "brief", "--assessment", "a", "--changed", "c", "--uncertainty", "u", "--next", "n", "--waiting-on", "w"}, true, false},
		{[]string{"goal", "done", "--summary", "s"}, true, false},
		{[]string{"goal", "wake", "30m", "check CI"}, true, false},
		{[]string{"new", "do it"}, true, true},
		{[]string{"send", "abc", "hi"}, true, true},
		{[]string{"item", "list"}, false, false},
		{[]string{"goal", "list"}, false, false},
	} {
		for _, off := range []bool{false, true} {
			if c.launches && !off {
				continue
			}
			t.Run(fmtArgs(c.args, off), func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("YAMNAYA_STATE_DIR", dir)
				t.Setenv("CODEX_THREAD_ID", self)
				s, err := core.OpenStore(filepath.Join(dir, "ym.db"))
				if err != nil {
					t.Fatal(err)
				}
				target := "someone-else"
				if off {
					target = self
				}
				if err := s.SetOff(target, true); err != nil {
					t.Fatal(err)
				}
				s.Close()
				root := NewRoot()
				var out bytes.Buffer
				root.SetOut(&out)
				root.SetErr(&out)
				root.SetArgs(c.args)
				err = root.Execute()
				if refused := errors.Is(err, core.ErrOff); refused != (off && c.refuse) {
					t.Fatalf("ym %v with ym off %v: %v, want refused %v", c.args, off, err, off && c.refuse)
				}
			})
		}
	}
}

func fmtArgs(args []string, off bool) string {
	name := args[0]
	if len(args) > 1 {
		name += " " + args[1]
	}
	if off {
		return name + "/off"
	}
	return name + "/on"
}
