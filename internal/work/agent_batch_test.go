package work_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func recipient(t *testing.T, b work.AgentBatch, agent string) work.AgentRecipient {
	t.Helper()
	for _, r := range b.Recipients {
		if r.Agent == agent {
			return r
		}
	}
	t.Fatalf("recipient %s missing from %+v", agent, b)
	return work.AgentRecipient{}
}

func TestManagerMessageTracksAcceptanceDeliveryAndRevisionBoundIncorporation(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	a, b, c := agent(t, s, "API"), agent(t, s, "SDK"), agent(t, s, "Console")
	for _, a := range []work.Agent{a, b, c} {
		settle(t, s, a)
	}
	text := "Prioritize caller compatibility; retain the current publication authority."
	batch, err := s.Work.SendAgentBatch("direction", "owner", text, []work.Agent{b, a, a})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Recipients) != 2 || recipient(t, batch, a.ID).Status() != "accepted" || recipient(t, batch, b.ID).Status() != "accepted" {
		t.Fatalf("wrong initial receipts: %+v", batch)
	}
	x, err := s.Work.AgentSnapshot(a.ID)
	if err != nil || len(x.Messages) != 1 || x.Messages[0].Text != text || x.Messages[0].Actor != "owner" {
		t.Fatalf("message absent from native snapshot: %+v %v", x.Messages, err)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	if recipient(t, batch, a.ID).Status() != "accepted" {
		t.Fatal("an operator reading a snapshot claimed native delivery")
	}
	if err = s.Work.AgentMessagesDelivered(x.Messages); err != nil {
		t.Fatal(err)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	if recipient(t, batch, a.ID).Status() != "delivered" {
		t.Fatal("delivery claimed incorporation or was lost")
	}
	if err = s.Work.Checkpoint(a.ID, "empty", work.Checkpoint{Brief: work.Brief{Assessment: "Seen"}, WaitFor: "Changes"}); err == nil {
		t.Fatal("empty checkpoint incorporated an owner message")
	}
	if _, err = s.Work.SendAgentBatch("later direction", "owner", "Keep the public API stable too", []work.Agent{a}); err != nil {
		t.Fatal(err)
	}
	if err = s.Work.Checkpoint(a.ID, "stale", work.Checkpoint{Brief: work.Brief{Assessment: "Seen", Context: x.Workspace.Context.Revision}, Inputs: x.Pending, WaitFor: "Changes"}); !errors.Is(err, work.ErrConflict) {
		t.Fatalf("obsolete input acknowledged a newer message: %v", err)
	}
	settle(t, s, a)
	x, _ = s.Work.AgentSnapshot(b.ID)
	if err = s.Work.AgentFailure(b.ID, x.Pending, errors.New("native messaging socket refused delivery")); err != nil {
		t.Fatal(err)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	if recipient(t, batch, a.ID).Status() != "incorporated" || recipient(t, batch, b.ID).Status() != "failed" {
		t.Fatalf("per-manager state collapsed: %+v", batch.Recipients)
	}
	if err = s.Work.DispatchAgentBatch(batch.ID, true); err != nil {
		t.Fatal(err)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	if recipient(t, batch, a.ID).Status() != "incorporated" || recipient(t, batch, b.ID).Status() != "accepted" {
		t.Fatal("retry resent successful recipients or dropped failed recipients")
	}
	if _, err = s.Work.SendAgentBatch("direction", "owner", text, []work.Agent{a, b}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Work.SendAgentBatch("direction", "owner", "Different direction", []work.Agent{a, b}); err == nil {
		t.Fatal("reused message identity changed the payload")
	}
	if _, err = s.Work.SendAgentBatch("direction", "owner", text, []work.Agent{a, c}); err == nil {
		t.Fatal("reused message identity changed the recipients")
	}
	for _, a := range []work.Agent{a, b, c} {
		history, err := s.Work.History(a.Workspace, 0)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range history {
			if e.Type == "message" && e.Actor == "owner" && e.Text == text {
				n++
			}
		}
		want := 1
		if a.ID == c.ID {
			want = 0
		}
		if n != want {
			t.Fatalf("manager %s received %d events, expected %d", a.Name, n, want)
		}
	}
}

func TestManagerMessageRetainsMissingRecipientsAcrossRestartUntilExplicitRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s := open(t, path)
	a, b := agent(t, s, "API"), agent(t, s, "SDK")
	for _, a := range []work.Agent{a, b} {
		settle(t, s, a)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var raw []byte
	if err = db.QueryRow(`SELECT state FROM ym_agents WHERE id=?`, b.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM ym_agents WHERE id=?`, b.ID); err != nil {
		t.Fatal(err)
	}
	batch, err := s.Work.SendAgentBatch("frozen recipients", "owner", "Keep the caller promise", []work.Agent{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if recipient(t, batch, a.ID).Status() != "accepted" || recipient(t, batch, b.ID).Status() != "failed" {
		t.Fatal("missing recipient failed the entire message or was silently dropped")
	}
	s.Close()
	s = open(t, path)
	if _, err = db.Exec(`INSERT INTO ym_agents(id,state) VALUES(?,?)`, b.ID, raw); err != nil {
		t.Fatal(err)
	}
	if err = s.Work.DispatchAgentBatches(); err != nil {
		t.Fatal(err)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	if recipient(t, batch, b.ID).Status() != "failed" {
		t.Fatal("restart silently retried a failed recipient")
	}
	if err = s.Work.DispatchAgentBatch(batch.ID, true); err != nil {
		t.Fatal(err)
	}
	batch, _ = s.Work.AgentBatch(batch.ID)
	if recipient(t, batch, b.ID).Status() != "accepted" {
		t.Fatal("corrected recipient did not accept the original message")
	}
	for _, a := range []work.Agent{a, b} {
		x, _ := s.Work.AgentSnapshot(a.ID)
		if len(x.Messages) != 1 || x.Messages[0].Text != "Keep the caller promise" {
			t.Fatalf("retry changed or duplicated the frozen message: %+v", x.Messages)
		}
	}
}
