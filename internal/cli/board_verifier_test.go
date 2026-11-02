package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestVerifierOwnershipUsesTheExportedBoardSnapshot(t *testing.T) {
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, StateDir: t.TempDir(), Procs: func() ([]proc.Proc, error) { return nil, nil }}
	v, err := st.AddVerification(core.Verification{Goal: "goal46", Session: "original"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	v.Session = "replacement"
	if err := st.SaveVerification(v); err != nil {
		t.Fatal(err)
	}
	j, err := hostBoard(c, b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(j.VerifierSessions, b.VerifierSessions) || len(j.VerifierSessions) != 1 {
		t.Fatalf("export reread association: %v", j.VerifierSessions)
	}
	raw, err := json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	var wire boardJSON
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	r := remoteBoard("box", wire)
	if !reflect.DeepEqual(r.VerifierSessions, b.VerifierSessions) {
		t.Fatalf("wire lost verifier association: %v", r.VerifierSessions)
	}
	if got := r.VerifierSessions["original"]; got.Goal != "goal46" || got.Verification != v.ID {
		t.Fatalf("wire binding=%+v", got)
	}
}
