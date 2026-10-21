package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestManagerBroadcastDeliveryRetainsFailuresAndDoesNotResendSuccessfulManagers(t *testing.T) {
	t.Setenv("YAMNAYA_STATE_DIR", t.TempDir())
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var agents []work.Agent
	b := core.Board{}
	for _, name := range []string{"API", "SDK"} {
		a, err := s.Work.CreateAgent(name, "manager", "", "Keep compatibility", "Investigate; reserve publication for the owner", work.Execution{Harness: "codex", Directory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		a.Session = "native-" + name
		if err = s.Work.SaveAgent(a); err != nil {
			t.Fatal(err)
		}
		agents = append(agents, a)
		b.Sessions = append(b.Sessions, core.Live{Session: core.Session{ID: a.Session}})
	}
	batch, err := s.Work.SendAgentBatch("broadcast", "owner", "Keep the existing caller contract", agents)
	if err != nil {
		t.Fatal(err)
	}
	broken := true
	deliveries := map[string]int{}
	c := &core.Core{Store: s, BackgroundSend: func(l core.Live, prompt string) error {
		deliveries[l.ID]++
		if !strings.Contains(prompt, "Keep the existing caller contract") || !strings.Contains(prompt, `"Messages"`) {
			t.Fatal("native delivery lost the tracked message payload")
		}
		if broken && l.ID == agents[1].Session {
			return errors.New("native messaging socket unavailable")
		}
		return nil
	}}
	for range 2 {
		if err = reconcileAgents(c, b); err != nil {
			t.Fatal(err)
		}
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	for _, r := range batch.Recipients {
		want := "delivered"
		if r.Agent == agents[1].ID {
			want = "failed"
		}
		if r.Status() != want || !r.Incorporated.IsZero() {
			t.Fatalf("transport claimed incorporation or hid failure: %+v", r)
		}
	}
	if deliveries[agents[0].Session] != 1 || deliveries[agents[1].Session] != 1 {
		t.Fatal("reconciliation repeatedly delivered unchanged or failed input")
	}
	broken = false
	if err = s.Work.DispatchAgentBatch(batch.ID, true); err != nil {
		t.Fatal(err)
	}
	if err = reconcileAgents(c, b); err != nil {
		t.Fatal(err)
	}
	if deliveries[agents[0].Session] != 1 || deliveries[agents[1].Session] != 2 {
		t.Fatalf("retry recipients changed: %+v", deliveries)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	for _, r := range batch.Recipients {
		if r.Status() != "delivered" || !r.Incorporated.IsZero() {
			t.Fatal("successful retry did not retain a transport-only receipt")
		}
	}
}
