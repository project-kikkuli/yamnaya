package core

import (
	"os"
	"strings"
	"testing"
)

func TestANewChiefIsToldItsRemitAndKeepsItThroughABriefAndARelaunch(t *testing.T) {
	w := newOrgWorld(t)
	remit := "Get the importer and the exporter shipping CSV by Friday."
	chief, err := w.core.NewChief("data", remit, "/repo/data", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	launch := w.launches.starts[len(w.launches.starts)-1]
	if !strings.Contains(launch.Prompt, remit) || !strings.Contains(launch.Prompt, "do not wait for instructions") || !strings.Contains(launch.Prompt, "ym goal --repo") {
		t.Fatalf("the chief's launch prompt does not hand it its remit to act on:\n%s", launch.Prompt)
	}
	if _, err := w.core.WriteBrief(chief.Session, Brief{Assessment: "planning", Changed: "started", Uncertainty: "none", Next: "start goals", WaitingOn: "none"}); err != nil {
		t.Fatal(err)
	}
	if page, _ := os.ReadFile(chief.BriefPath); strings.Contains(string(page), remit) {
		t.Fatal("the brief rewrite left the seeded page; the test proves nothing")
	}
	org, _ := w.core.LoadOrg()
	if got := org.Managers[chief.ID].Remit; got != remit {
		t.Fatalf("remit after a brief = %q, want %q", got, remit)
	}
	w.stop(chief.Session)
	w.core.relaunch(w.board(t), org, org.Managers[chief.ID])
	again := w.launches.starts[len(w.launches.starts)-1]
	if again.Prompt == launch.Prompt || !strings.Contains(again.Prompt, "relaunched") || !strings.Contains(again.Prompt, remit) {
		t.Fatalf("the relaunch prompt lost the remit:\n%s", again.Prompt)
	}
	m, text, rev, err := w.core.Context(chief.ID)
	if err != nil || m.ID != chief.ID || text != remit || rev != 0 {
		t.Fatalf("Context(%s) = %s, %q, %d, %v; want the remit as revision 0", chief.ID, m.ID, text, rev, err)
	}
}

func TestOpeningTheStoreRecoversAnOlderChiefsRemitFromItsSeededBrief(t *testing.T) {
	w := newOrgWorld(t)
	seeded, err := w.core.NewChief("data", "Ship CSV.\n\nKeep the schema stable.", "/repo/data", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	briefed, err := w.core.NewChief("ops", "Keep the fleet green.", "/repo/ops", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.WriteBrief(briefed.Session, Brief{Assessment: "a", Changed: "b", Uncertainty: "none", Next: "c", WaitingOn: "none"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.db.Exec(`UPDATE ym_managers SET remit = ''`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := chiefRemits(w.core.Store.db); err != nil {
			t.Fatal(err)
		}
	}
	w.core.Store.invalidate()
	if got, _ := w.core.Store.Manager(seeded.ID); got.Remit != "Ship CSV.\n\nKeep the schema stable." {
		t.Fatalf("recovered remit = %q", got.Remit)
	}
	if got, _ := w.core.Store.Manager(briefed.ID); got.Remit != "" {
		t.Fatalf("a remit was read from a page a brief rewrote: %q", got.Remit)
	}
}
