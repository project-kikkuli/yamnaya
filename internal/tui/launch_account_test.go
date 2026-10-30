package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestTheLaunchPromptCyclesEveryAccountAndTabKeepsACompatibleOne(t *testing.T) {
	accounts := []core.LoginProfile{
		{Harness: "claude", ID: "default"}, {Harness: "claude", ID: "claude-work"}, {Harness: "codex", ID: "default"},
	}
	for _, c := range []struct {
		name          string
		keys          []string
		harness, want string
	}{
		{"one step picks the first account", []string{"shift+tab"}, "claude", "default"},
		{"the second claude account is reachable", []string{"shift+tab", "shift+tab"}, "claude", "claude-work"},
		{"a codex account switches the harness", []string{"shift+tab", "shift+tab", "shift+tab"}, "codex", "default"},
		{"the cycle wraps to auto", []string{"shift+tab", "shift+tab", "shift+tab", "shift+tab"}, "codex", "auto"},
		{"tab keeps an account the other harness has", []string{"shift+tab", "tab"}, "codex", "default"},
		{"tab drops an account the other harness lacks", []string{"shift+tab", "shift+tab", "tab"}, "codex", "auto"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			k.Launches = &launches{}
			k.ProfileList = func() []core.LoginProfile { return accounts }
			m = to(t, place(t, m, k), mission("lead"))
			m = press(t, m, append([]string{"N"}, c.keys...)...)
			v := ansi.Strip(strings.Join(m.footer(), "\n"))
			if !strings.Contains(v, "new "+c.harness+" ") || !strings.Contains(v, "account: "+c.want+" ⇧⇥") {
				t.Fatalf("prompt does not show %s on %s:\n%s", c.want, c.harness, v)
			}
		})
	}
}
