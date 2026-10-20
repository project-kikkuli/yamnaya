package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestManagedIdentitySurvivesShellEnvironmentChanges(t *testing.T) {
	for _, mode := range []string{"explicit identity", "native environment", "native ancestry", "owner terminal"} {
		t.Run(mode, func(t *testing.T) {
			for _, name := range []string{"YAMNAYA_AGENT_ID", "CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID"} {
				t.Setenv(name, "")
			}
			s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			a, err := s.Work.CreateAgent("General", "chief", "", "Carry context", "Investigate; reserve owner judgments", work.Execution{Harness: "codex", Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			a.Session = "06456f12-aaaa"
			if err = s.Work.SaveAgent(a); err != nil {
				t.Fatal(err)
			}
			c := &core.Core{Store: s, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			want := a.ID
			switch mode {
			case "explicit identity":
				t.Setenv("YAMNAYA_AGENT_ID", a.ID)
			case "native environment":
				t.Setenv("CODEX_THREAD_ID", a.Session)
			case "native ancestry":
				c.Harnesses = []core.Harness{agent{}}
				c.Procs = func() ([]proc.Proc, error) {
					return []proc.Proc{{PID: os.Getpid(), PPID: 42, Args: []string{"ym"}}, {PID: 42, PPID: 1, Args: []string{"claude"}}}, nil
				}
			default:
				want = "owner"
			}
			if got := (&env{core: c}).actor(); got != want {
				t.Fatalf("management actor %s, want %s", got, want)
			}
		})
	}
}
