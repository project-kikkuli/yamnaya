package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// TestReloadedBoardMatchesAFreshRead keeps one core reloading as the files under it change and compares every board with one a new core reads from nothing.
func TestReloadedBoardMatchesAFreshRead(t *testing.T) {
	home := t.TempDir()
	if err := os.CopyFS(home, os.DirFS(filepath.Join("..", "harness", "claude", "testdata", "home"))); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "projects", "-Users-me-src-app")
	crew := filepath.Join(project, "11111111-aaaa-4aaa-8aaa-111111111111", "subagents")
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Now().Add(time.Minute)
	clock := func() time.Time { return now }
	procs := []proc.Proc{
		{PID: 101, PPID: 1, TTY: "/dev/ttys001", Args: []string{"claude"}},
		{PID: 103, PPID: 1, TTY: "/dev/ttys003", Args: []string{"claude", "--resume", "33333333-cccc-4ccc-8ccc-333333333333"}},
	}
	newCore := func(cache core.ScanCache) *core.Core {
		a := &claude.Adapter{Dir: home, Bin: "claude", Cwd: func(int) string { return "" }, Now: clock, Cache: cache}
		return &core.Core{Store: st, Harnesses: []core.Harness{a}, Now: clock, Procs: func() ([]proc.Proc, error) { return procs, nil }}
	}
	reloading := newCore(st)
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	appendTo := func(path, text string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.WriteString(text); err != nil {
			t.Fatal(err)
		}
	}
	user := func(text string) string {
		return `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}` + "\n"
	}
	steps := []struct {
		name   string
		change func()
		want   func(core.Board) bool
	}{
		{"first read", func() {}, func(b core.Board) bool { return len(b.Sessions) == 2 }},
		{"transcript append", func() {
			appendTo(filepath.Join(project, "33333333-cccc-4ccc-8ccc-333333333333.jsonl"), user("Then ship it"))
		}, func(b core.Board) bool { return len(b.Sessions) == 2 }},
		{"new session and new subagent", func() {
			write(filepath.Join(project, "44444444-dddd-4ddd-8ddd-444444444444.jsonl"), user("Fix the flaky build"))
			write(filepath.Join(crew, "agent-a4.meta.json"), `{"agentType":"general-purpose","description":"Bisect the failure"}`)
			write(filepath.Join(crew, "agent-a4.jsonl"), user("Find the bad commit"))
			procs = append(procs, proc.Proc{PID: 104, PPID: 1, TTY: "/dev/ttys004", Args: []string{"claude", "--resume", "44444444-dddd-4ddd-8ddd-444444444444"}})
		}, func(b core.Board) bool { return len(b.Sessions) == 3 }},
		{"subagent append and meta rewrite", func() {
			appendTo(filepath.Join(crew, "agent-a4.jsonl"), `{"type":"assistant","message":{"content":[],"stop_reason":"end_turn"}}`+"\n")
			write(filepath.Join(crew, "agent-a1.meta.json"), `{"agentType":"general-purpose","description":"Run the whole test suite"}`)
		}, func(b core.Board) bool { return true }},
		{"ended process", func() {
			procs = procs[:1:1]
			procs = append(procs, proc.Proc{PID: 104, PPID: 1, TTY: "/dev/ttys004", Args: []string{"claude", "--resume", "44444444-dddd-4ddd-8ddd-444444444444"}})
		}, func(b core.Board) bool { return len(b.Sessions) == 2 }},
		{"rotated and truncated files", func() {
			path := filepath.Join(project, "44444444-dddd-4ddd-8ddd-444444444444.jsonl")
			write(path+".new", user("Rewrite the release notes")+user("And tag the release with a longer prompt than before"))
			if err := os.Rename(path+".new", path); err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(crew, "agent-a4.jsonl"), user("Go"))
		}, func(b core.Board) bool { return len(b.Sessions) == 2 }},
	}
	for _, s := range steps {
		s.change()
		got, err := reloading.Board()
		if err != nil {
			t.Fatal(err)
		}
		want, err := newCore(nil).Board()
		if err != nil {
			t.Fatal(err)
		}
		if !s.want(want) {
			t.Fatalf("%s: the scripted change did not reach the board: %+v", s.name, want.Sessions)
		}
		if !reflect.DeepEqual(got.Sessions, want.Sessions) || !reflect.DeepEqual(got.Projects, want.Projects) || !reflect.DeepEqual(got.Needs, want.Needs) {
			t.Fatalf("%s: reloaded board\n%+v\nfresh board\n%+v", s.name, got.Sessions, want.Sessions)
		}
	}
}
