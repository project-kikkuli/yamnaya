package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestExplicitOwnerAndReviewCommandsKeepTheirRequests(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		kind string
		fail bool
	}{
		{"review", []string{"review", "https://claude.ai/artifact/plan", "--question", "review direction", "--rec", "approve"}, core.KindReview, false},
		{"owner decision", []string{"add", "--to", "owner", "--kind", "decision", "--question", "choose direction", "--rec", "a", "--option", "a=adopt", "--option", "b=revise"}, core.KindDecision, false},
		{"unsupported destination", []string{"add", "--to", "nobody", "--kind", "go", "--question", "ship?"}, "", true},
		{"review lacks document", []string{"review", " ", "--question", "review", "--rec", "approve"}, "", true},
		{"review lacks recommendation", []string{"review", "https://claude.ai/artifact/plan", "--question", "review"}, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			t.Setenv("CODEX_THREAD_ID", "writer")
			k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) {
				return []proc.Proc{{PID: os.Getpid(), Args: []string{"sh"}}}, nil
			}}
			with := func(run runFunc) func(*cobra.Command, []string) error {
				return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
			}
			cmd := itemCmd(with, with)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs(tc.args)
			if err := cmd.Execute(); (err != nil) != tc.fail {
				t.Fatalf("command result: %v, output %s", err, out.String())
			}
			items, err := st.Items(core.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			if tc.fail {
				if len(items) != 0 {
					t.Fatalf("refused request was stored: %+v", items)
				}
				return
			}
			if len(items) != 1 || items[0].Kind != tc.kind || items[0].Recipient != core.Owner || items[0].Session != "writer" {
				t.Fatalf("wrong request stored: %+v", items)
			}
			if tc.kind == core.KindReview && (len(items[0].Evidence) != 1 || items[0].Evidence[0] != tc.args[1]) {
				t.Fatalf("review lost document: %+v", items[0])
			}
		})
	}
}
