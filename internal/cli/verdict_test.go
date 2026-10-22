package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestOnlyTheVerifierSessionItselfRecordsAVerdict(t *testing.T) {
	const verifier = "5e1f1e55-0000-4000-8000-000000000001"
	for _, c := range []struct {
		name string
		args []string
		want string
	}{
		{"--session naming the verifier", []string{"--session", verifier}, "without --session"},
		{"--session naming a prefix of the verifier", []string{"--session", verifier[:8]}, "without --session"},
		{"a caller that is not the verifier", nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", dir)
			s, err := core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			g, err := s.AddGoal(core.Goal{Ask: "ship it", Repo: dir, State: core.GoalVerifying})
			if err != nil {
				t.Fatal(err)
			}
			v, err := s.AddVerification(core.Verification{Goal: g.ID, Summary: "shipped"})
			if err != nil {
				t.Fatal(err)
			}
			v.Session = verifier
			if err := s.SaveVerification(v); err != nil {
				t.Fatal(err)
			}
			s.Close()

			root := NewRoot()
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&out)
			root.SetArgs(append([]string{"goal", "verdict", g.ID, "--pass", "--finding", "looks fine"}, c.args...))
			err = root.Execute()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("verdict: %v, want an error containing %q", err, c.want)
			}

			s, err = core.OpenStore(filepath.Join(dir, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if open, err := s.OpenVerification(g.ID); err != nil || open.Verdict != "" {
				t.Fatalf("verification after a refused verdict: %+v, %v", open, err)
			}
			if got, _ := s.Goal(g.ID); got.State != core.GoalVerifying {
				t.Fatalf("goal is %s after a refused verdict", got.State)
			}
		})
	}
}
