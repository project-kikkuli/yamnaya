package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func TestManagerCanInspectReportAndAcceptExplicitAssignments(t *testing.T) {
	manager := "01a16a70-a4c4-7670-a0a2-e092c1f5e145"
	t.Setenv("CODEX_THREAD_ID", manager)
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	k := &core.Core{Store: st, Procs: func() ([]proc.Proc, error) {
		return []proc.Proc{{PID: os.Getpid(), PPID: 1, Args: []string{"sh"}}}, nil
	}}
	g, err := st.AddGoal(core.Goal{Ask: "implement CSV"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.AddManager(core.Manager{Role: core.RoleManager, GoalID: g.ID, Session: manager})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	g.ManagerID = m.ID
	if err = st.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
	}
	run := func(wantError bool, args ...string) string {
		t.Helper()
		cmd := goalCmd(with, with)
		var out bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&out)
		cmd.SetArgs(args)
		cmd.SilenceUsage = true
		cmd.SilenceErrors = true
		err := cmd.Execute()
		if (err != nil) != wantError {
			t.Fatalf("goal %v: %v %s", args, err, out.String())
		}
		return out.String()
	}
	run(false, "assign", "planned-worker", "--planned", "--source", "build:one", "--reason", "implement CSV")
	assignments, err := st.Assignments(g.ID)
	if err != nil || len(assignments) != 1 {
		t.Fatalf("assignments: %+v %v", assignments, err)
	}
	id := assignments[0].ID
	if got := run(false, "assignments"); !strings.Contains(got, id) || !strings.Contains(got, "outstanding") {
		t.Fatalf("assignment not inspectable: %s", got)
	}
	run(true, "done", "--summary", "finished")
	report := run(false, "report", id, "--summary", "worker exited after committing CSV support")
	assignments, err = st.Assignments(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	event := assignments[0].Report
	if !strings.Contains(report, fmt.Sprintf("ym goal accept %s --report %d", id, event)) {
		t.Fatalf("missing actionable acceptance command: %s", report)
	}
	if text := run(false, "event", fmt.Sprint(event)); !strings.Contains(text, "CSV support") {
		t.Fatalf("missing terminal evidence: %s", text)
	}
	run(false, "accept", id, "--report", fmt.Sprint(event))
	if got := run(false, "assignments"); !strings.Contains(got, "accepted") {
		t.Fatalf("accepted obligation invisible: %s", got)
	}
	run(false, "done", "--summary", "CSV complete")
	run(false, "resume", "--source", "chat:two", "--reason", "add quoted fields")
	current, err := st.Goal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != core.GoalActive || current.WorkRevision != 2 {
		t.Fatalf("direct chat resume: %+v", current)
	}
}
