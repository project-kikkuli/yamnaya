package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestAgentCheckpointProcessFixture(t *testing.T) {
	if os.Getenv("YM_AGENT_PROCESS_FIXTURE") != "1" {
		t.Skip("subprocess fixture")
	}
	dir := os.Getenv("YAMNAYA_STATE_DIR")
	mode, err := os.ReadFile(filepath.Join(dir, "mode"))
	if err != nil {
		t.Fatal(err)
	}
	if string(mode) == "fail" {
		os.Exit(23)
	}
	fmt.Println(`{"type":"thread.started","thread_id":"fixture-native"}`)
	s, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	x, err := s.Work.AgentSnapshot(os.Getenv("YAMNAYA_AGENT_ID"))
	if err != nil {
		t.Fatal(err)
	}
	c := work.Checkpoint{Brief: work.Brief{Assessment: "Fixture incorporated: " + x.Workspace.Context.Text, Context: x.Workspace.Context.Revision}, Inputs: x.Pending, WaitFor: "The next direction message", Next: "Inspect changed evidence"}
	raw, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(os.Getenv("YAMNAYA_AGENT_REPORT"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundAgentFailurePersistsUntilInvestigatedAndReplaced(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	t.Setenv("YM_AGENT_PROCESS_FIXTURE", "1")
	bin := filepath.Join(t.TempDir(), "ym")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ym")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %v: %s", err, out)
	}
	s, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	a, err := s.Work.CreateAgent("General", "chief", "", "Coordinate compatibility work", "Inspect, coordinate and prepare judgments", work.Execution{Harness: "codex", Directory: dir, Command: []string{os.Args[0], "-test.run=^TestAgentCheckpointProcessFixture$"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "mode"), []byte("fail"), 0600); err != nil {
		t.Fatal(err)
	}
	cycle := func() {
		t.Helper()
		out, err := exec.Command(bin, "serve", "--once", "--discover=false", "--github-requests=false").CombinedOutput()
		if err != nil {
			t.Fatalf("cycle %v: %s", err, out)
		}
	}
	await := func(wantFailure bool, context int64) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			current, err := s.Work.Agent(a.ID)
			if err == nil && !current.Running {
				w, _ := s.Work.Get(a.Workspace)
				if wantFailure && current.Error != "" || !wantFailure && w.Brief.Context == context && strings.HasPrefix(w.Brief.Assessment, "Fixture incorporated:") {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		raw, _ := os.ReadFile(filepath.Join(dir, "agent-"+a.ID+".log"))
		t.Fatalf("agent did not reach expected state: %s", raw)
	}
	cycle()
	await(true, 0)
	x, err := s.Work.AgentSnapshot(a.ID)
	if err != nil || len(x.Pending) == 0 {
		t.Fatal("execution failure discharged commitment")
	}
	due, err := s.Work.DueAgents()
	if err != nil || len(due) != 0 {
		t.Fatal("failed execution was blindly retried")
	}
	if err = os.WriteFile(filepath.Join(dir, "mode"), []byte("checkpoint"), 0600); err != nil {
		t.Fatal(err)
	}
	replace := exec.Command(bin, "agent", "replace", a.ID)
	if out, err := replace.CombinedOutput(); err != nil {
		t.Fatalf("replace %v: %s", err, out)
	}
	w, _ := s.Work.Get(a.Workspace)
	w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "context", Actor: "owner", Text: "Preserve nested callers"})
	if err != nil {
		t.Fatal(err)
	}
	cycle()
	await(false, w.Context.Revision)
	w, err = s.Work.Get(a.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if !w.BriefCurrent() || len(w.Brief.Inputs) == 0 {
		t.Fatal("recovery did not publish a current, input-bound brief")
	}
	x, _ = s.Work.AgentSnapshot(a.ID)
	if x.Agent.Session != "fixture-native" {
		t.Fatal("native identity was not recorded from the execution stream")
	}
	if len(x.Pending) != 0 {
		t.Fatalf("incorporated direction still pending: %+v", x.Pending)
	}
	history, err := s.Work.History(a.Workspace, 0)
	if err != nil {
		t.Fatal(err)
	}
	sawFailure := false
	for _, e := range history {
		if strings.Contains(e.Text, "Execution needs investigation") {
			sawFailure = true
		}
	}
	if !sawFailure {
		t.Fatal(fmt.Sprint("lost the reason for replacement: ", history))
	}
}
