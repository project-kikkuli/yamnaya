package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestWorkManagerHelper(t *testing.T) {
	id := os.Getenv("YM_FIXTURE_WORK")
	if id == "" {
		t.Skip("process fixture only")
	}
	s, err := core.OpenStore(filepath.Join(os.Getenv("YAMNAYA_STATE_DIR"), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	updates, err := s.Work.Inbox("fixture-manager")
	if err != nil || len(updates) == 0 {
		t.Fatalf("no manager inflow: %v", err)
	}
	w, err := s.Work.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Work.Commit(work.Event{Work: id, Expected: w.Revision, Type: "adopt", Actor: "fixture-manager", Context: w.Context.Revision})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Work.Commit(work.Event{Work: id, Expected: w.Revision, Type: "brief", Actor: "fixture-manager", Brief: work.Brief{Assessment: "The new direction is incorporated: " + w.Context.Text, Changed: "Updated after the context message", Rationale: "Preserve the agreed contract", Next: "Continue compatibility checks", Context: w.Context.Revision}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBackgroundProcessIncorporatesDirectionAndServesTheBriefWithoutATUI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	bin := filepath.Join(t.TempDir(), "ym")
	build := exec.Command("go", "build", "-o", bin, "./cmd/ym")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	s, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.Work.Ensure("fixture", "/src/query/.git", "Compatibility")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("YM_FIXTURE_WORK", w.ID)
	apply := func(e work.Event) {
		t.Helper()
		e.Work, e.Expected = w.ID, w.Revision
		w, err = s.Work.Commit(e)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(work.Event{Type: "bind", Target: "fixture-manager", Execution: &work.Execution{Harness: "fixture", Directory: dir, Command: []string{os.Args[0], "-test.run=^TestWorkManagerHelper$"}}})
	apply(work.Event{Type: "manager", Target: "fixture-manager"})
	apply(work.Event{Type: "context", Text: "Keep v2 callers compatible"})
	runOnce := func() {
		t.Helper()
		out, err := exec.Command(bin, "serve", "--once", "--discover=false", "--github-requests=false").CombinedOutput()
		if err != nil {
			t.Fatalf("background reconciliation: %v %s", err, out)
		}
	}
	waitFor := func(context int64) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			w, err = s.Work.Get(w.ID)
			if err == nil && w.Brief.Context == context && w.Bindings["fixture-manager"] == context {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		logs, _ := filepath.Glob(filepath.Join(dir, "work-run-*.log"))
		var detail []byte
		for _, path := range logs {
			b, _ := os.ReadFile(path)
			detail = append(detail, b...)
		}
		t.Fatalf("manager did not incorporate context %d: %+v %s", context, w, detail)
	}
	runOnce()
	waitFor(1)
	service := exec.Command(bin, "serve", "--discover=false", "--github-requests=false", "--listen", "127.0.0.1:0", "--interval", "25ms")
	service.Stderr = io.Discard
	if err = service.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if service.Process != nil {
			service.Process.Signal(syscall.SIGTERM)
			service.Wait()
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	address := ""
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(filepath.Join(dir, "work-service.json"))
		var info struct{ Address string }
		json.Unmarshal(raw, &info)
		address = info.Address
		if address != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if address == "" {
		t.Fatal("brief server did not publish its address")
	}
	apply(work.Event{Type: "context", Text: "Preserve the public API and retry semantics"})
	waitFor(2)
	response, err := http.Get(address + "/brief/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 200 || !strings.Contains(string(body), "Preserve the public API and retry semantics") {
		t.Fatalf("rich brief did not follow current assessment: %d %s %v", response.StatusCode, body, err)
	}
	nextBin := bin + ".new"
	contents, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(nextBin, contents, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(nextBin, bin); err != nil {
		t.Fatal(err)
	}
	firstAddress := address
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		raw, _ := os.ReadFile(filepath.Join(dir, "work-service.json"))
		var info struct{ Address string }
		json.Unmarshal(raw, &info)
		if info.Address != "" && info.Address != firstAddress {
			address = info.Address
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if address == firstAddress {
		t.Fatal("detached service did not reload a replaced binary")
	}
	status, err := exec.Command(bin, "service", "status").Output()
	if err != nil || !strings.Contains(string(status), `"running":true`) {
		t.Fatalf("service status: %s %v", status, err)
	}
	response, err = http.Get(address + "/brief/" + w.ID)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("brief missing after upgrade: %d", response.StatusCode)
	}
	if err = service.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	service.Wait()
	service.Process = nil
	latest, err := s.Work.Get(w.ID)
	if err != nil || latest.Brief.Context != 2 || latest.Context.Revision != 2 {
		t.Fatal("service exit lost durable context or assessment")
	}
	if latest.Reviews == nil {
		t.Fatal("obligation registry disappeared")
	}
	runOnce()
}
