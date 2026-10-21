package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func managers(t *testing.T) (Model, []work.Agent) {
	t.Helper()
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	c := &core.Core{Store: s, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	var agents []work.Agent
	for _, name := range []string{"API", "SDK", "Console"} {
		a, err := s.Work.CreateAgent(name, "manager", "", "Preserve caller compatibility", "Inspect and coordinate; publication requires owner approval", work.Execution{Harness: "codex", Directory: t.TempDir()})
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, a)
	}
	m := Model{core: c, mode: modeChiefs, width: 120, height: 40}
	return refresh(t, m), agents
}

func TestManagerSelectionSurvivesRefreshAndFilteringAndFreezesAtComposition(t *testing.T) {
	m, agents := managers(t)
	for _, a := range agents[:2] {
		m.cursor = 0
		m = to(t, m, func(r row) bool { return r.chief != nil && r.chief.ID == a.ID })
		m = press(t, m, " ")
	}
	m = refresh(t, m)
	m = press(t, m, "/", "API", "enter")
	if m.mode != modeChiefs || len(m.rows()) != 1 || len(m.chiefSelected) != 2 {
		t.Fatal("filtering changed selection identities or left management")
	}
	m = press(t, m, "s")
	if m.mode != modeInput || len(m.chiefRecipients) != 2 || !strings.Contains(m.chiefComposeView().String(), "API") || !strings.Contains(m.chiefComposeView().String(), "SDK") {
		t.Fatal("composer did not expose the complete frozen recipient list")
	}
	added, err := m.core.Store.Work.CreateAgent("New arrival", "manager", "", "Inspect changes", "Read only", work.Execution{Harness: "codex", Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m = press(t, m, "Keep caller compatibility first", "enter")
	if m.mode != modeChiefs {
		t.Fatal("message submit did not send directly and return to management")
	}
	for _, a := range append(agents, added) {
		x, err := m.core.Store.Work.AgentSnapshot(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := 0
		if a.ID == agents[0].ID || a.ID == agents[1].ID {
			want = 1
		}
		if len(x.Messages) != want {
			t.Fatalf("manager %s got %d messages, wanted %d", a.Name, len(x.Messages), want)
		}
	}
	m = press(t, m, "v")
	if m.mode != modeAgentBatch || !strings.Contains(m.agentBatchView().String(), "2 accepted") || !strings.Contains(m.agentBatchView().String(), "SDK") {
		t.Fatal("receipt view lost a filtered recipient or claimed incorporation")
	}
}

func TestCancellingManagerMessageDoesNotSendAndSingleRecipientStillWorks(t *testing.T) {
	m, agents := managers(t)
	m = to(t, m, func(r row) bool { return r.chief != nil && r.chief.ID == agents[0].ID })
	m = press(t, m, "s", "Do not send this", "esc")
	batches, err := m.core.Store.Work.AgentBatches()
	if err != nil || len(batches) != 0 {
		t.Fatal("cancelling created message intent or delivery")
	}
	m = press(t, m, "s", "Retain the API contract", "enter")
	batches, err = m.core.Store.Work.AgentBatches()
	if err != nil || len(batches) != 1 || len(batches[0].Recipients) != 1 || batches[0].Recipients[0].Agent != agents[0].ID || batches[0].Text != "Retain the API contract" {
		t.Fatalf("single-recipient message changed: %+v %v", batches, err)
	}
}

func TestChangedManagerWorkspaceFailsItsFrozenRecipientAndRetryDoesNotResendOthers(t *testing.T) {
	m, agents := managers(t)
	for _, a := range agents[:2] {
		m.cursor = 0
		m = to(t, m, func(r row) bool { return r.chief != nil && r.chief.ID == a.ID })
		m = press(t, m, " ")
	}
	m = press(t, m, "s")
	changed := agents[1]
	changed.Workspace = agents[2].Workspace
	if err := m.core.Store.Work.SaveAgent(changed); err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m = press(t, m, "Keep the original caller promise", "enter", "v")
	if !strings.Contains(m.agentBatchView().String(), "1 failed") || !strings.Contains(m.agentBatchView().String(), "workspace changed") {
		t.Fatal("recipient disappearance or reassignment was hidden")
	}
	if err := m.core.Store.Work.SaveAgent(agents[1]); err != nil {
		t.Fatal(err)
	}
	m = press(t, m, "r")
	if !strings.Contains(m.agentBatchView().String(), "2 accepted") {
		t.Fatal("failed recipient could not be retried after correcting its identity")
	}
	for _, a := range agents[:2] {
		x, err := m.core.Store.Work.AgentSnapshot(a.ID)
		if err != nil || len(x.Messages) != 1 {
			t.Fatalf("retry duplicated or lost manager %s message: %+v %v", a.Name, x.Messages, err)
		}
	}
}
