package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/tui"
)

// agentOnly are commands agents or setup run, never the owner at the board.
var agentOnly = map[string]bool{
	"ym item add": true, "ym item review": true, "ym item inbox": true, "ym item prepare": true, "ym spawn": true, "ym limits record": true,
	"ym install": true, "ym completion": true, "ym board": true, "ym help": true, "ym serve": true, "ym service ensure": true, "ym slot": true,
	"ym goal done": true, "ym goal verdict": true, "ym goal artifact": true, "ym goal update": true, "ym goal wake": true, "ym goal event": true,
	"ym goal adopt": true, "ym goal brief": true,
	"ym goal resume": true, "ym goal assign": true, "ym goal assignments": true, "ym goal report": true, "ym goal accept": true, "ym goal bind": true,
}

func TestEveryOwnerCommandHasABoardKeyInHelp(t *testing.T) {
	var walk func(c *cobra.Command)
	seen := map[string]bool{}
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			if sub.Hidden {
				continue
			}
			path := sub.CommandPath()
			if strings.HasPrefix(path, "ym completion") || agentOnly[path] {
				continue
			}
			if sub.Runnable() {
				seen[path] = true
				key, ok := tui.Commands[path]
				if !ok {
					t.Errorf("%s has no board key", path)
				} else if !strings.Contains(tui.Help, "\n   "+key+" ") {
					t.Errorf("%s maps to %q, which ? help does not list", path, key)
				}
			}
			walk(sub)
		}
	}
	root := NewRoot()
	root.InitDefaultCompletionCmd()
	walk(root)
	for path := range tui.Commands {
		if !seen[path] {
			t.Errorf("board key for %s, which is no owner command", path)
		}
	}
}
