package launch

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
)

type nativePane struct {
	host    *tmux.Host
	handle  core.Handle
	adapter *codex.Adapter
}

func (p nativePane) Prompt() (bool, bool, error) {
	text, err := p.host.Screen(p.handle)
	if err != nil {
		return false, false, err
	}
	_, shown := p.adapter.ParsePrompt(text)
	return shown, true, nil
}
func (p nativePane) Type(text string) error { return core.Type(p.host, p.handle, p.adapter, 0, text) }

func TestNativeCodexMultilineDelivery(t *testing.T) {
	if os.Getenv("YM_NATIVE_CODEX_DELIVERY_TEST") != "1" {
		t.Skip("opt-in offline native UI check")
	}
	if runtime.GOOS != "darwin" {
		t.Skip("requires macOS sandbox-exec")
	}
	wrapper, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	bin := codex.Native(wrapper)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := filepath.Join(root, "codex")
	for _, p := range []string{config, filepath.Join(root, "tmp"), filepath.Join(root, "work")} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	profile := filepath.Join(root, "offline.sb")
	rules := fmt.Sprintf(`(version 1)
(allow default)
(deny network*)
(deny file-read* (subpath %q) (subpath %q) (literal %q) (subpath %q))
(deny process-exec (literal "/usr/bin/security"))
`, filepath.Join(home, ".codex"), filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json"), filepath.Join(home, "Library/Keychains"))
	if err := os.WriteFile(profile, []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(root, "tmux.conf")
	if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0600); err != nil {
		t.Fatal(err)
	}
	h := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
	args := []string{"/usr/bin/sandbox-exec", "-f", profile, "/usr/bin/env", "-i", "PATH=/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + root, "CODEX_HOME=" + config, "CODEX_SQLITE_HOME=" + config, "TMPDIR=" + filepath.Join(root, "tmp") + "/", "TERM=xterm-256color", "LANG=en_US.UTF-8", bin, "--no-daemon", "--yolo", "--no-alt-screen"}
	for _, value := range []string{`cli_auth_credentials_store="ephemeral"`, `check_for_update_on_startup=false`, `analytics.enabled=false`, `otel.exporter="none"`, `model="offline-fixture"`, `model_provider="fixture"`, `model_providers.fixture.name="fixture"`, `model_providers.fixture.base_url="http://127.0.0.1:9/v1"`, `model_providers.fixture.wire_api="responses"`, `model_providers.fixture.requires_openai_auth=false`, `model_providers.fixture.request_max_retries=0`, `model_providers.fixture.stream_max_retries=0`, fmt.Sprintf("projects.%q.trust_level=\"trusted\"", filepath.Join(root, "work"))} {
		args = append(args, "-c", value)
	}
	pane, err := h.Start(conf, "offline-delivery", filepath.Join(root, "work"), args)
	if err != nil {
		t.Fatal(err)
	}
	p := nativePane{host: h, handle: core.Handle{ID: pane.ID}, adapter: &codex.Adapter{}}
	var initial string
	ready := false
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		initial, _ = h.Screen(p.handle)
		if _, ok := codex.Composer(initial); ok {
			ready = true
			break
		}
		if current, e := h.Pane("offline-delivery"); e == nil && current.Dead {
			break
		}
	}
	if !ready {
		t.Fatalf("native UI did not show composer:\n%s", initial)
	}
	text := "YM_OFFLINE_DELIVERY_ONCE\n\n" + strings.Repeat("Audit evidence stays local. No inference is available.\n", 40) + "\nEnd of multiline delivery."
	st := store(t)
	snd := Sender{Box: st}
	m, out, err := snd.Send("offline-native", p, text)
	if err != nil || out != Typed {
		screen, _ := h.Screen(p.handle)
		t.Fatalf("Send outcome %v, %v; screen:\n%s", out, err, screen)
	}
	for range 3 {
		if ok, err := snd.Hold(m.Seq, p); ok || err != nil {
			t.Fatalf("Hold repeated delivery: %v, %v", ok, err)
		}
	}
	final, _ := h.Screen(p.handle)
	box, ok := codex.Composer(final)
	if !ok || strings.Contains(box, "YM_OFFLINE_DELIVERY_ONCE") || strings.Contains(box, "[Pasted") {
		t.Fatalf("composer did not clear: %q\n%s", box, final)
	}
	count := 0
	var receipts []json.RawMessage
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		count = 0
		receipts = nil
		paths, _ := filepath.Glob(filepath.Join(config, "sessions", "*", "*", "*", "*.jsonl"))
		for _, path := range paths {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			scan := bufio.NewScanner(f)
			scan.Buffer(make([]byte, 4096), 4<<20)
			for scan.Scan() {
				var line struct {
					Type    string `json:"type"`
					Payload struct {
						Type string `json:"type"`
						Item struct {
							Type    string `json:"type"`
							Content []struct {
								Text string `json:"text"`
							} `json:"content"`
						} `json:"item"`
					} `json:"payload"`
				}
				if json.Unmarshal(scan.Bytes(), &line) == nil && line.Type == "event_msg" && line.Payload.Type == "item_completed" && line.Payload.Item.Type == "UserMessage" {
					var parts []string
					for _, part := range line.Payload.Item.Content {
						parts = append(parts, part.Text)
					}
					if strings.Join(parts, "") == text {
						count++
						receipts = append(receipts, append(json.RawMessage{}, scan.Bytes()...))
					}
				}
			}
			err = scan.Err()
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		if count > 0 {
			break
		}
	}
	if count != 1 {
		t.Fatalf("canonical native UserMessage receipts=%d, want 1", count)
	}
	if dir := os.Getenv("YM_NATIVE_DELIVERY_EVIDENCE"); dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.MarshalIndent(receipts, "", "  ")
		for name, data := range map[string][]byte{"native-receipts.json": data, "native-initial.txt": []byte(initial), "native-final.txt": []byte(final), "native-sandbox.sb": []byte(rules)} {
			if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Log("one canonical native UserMessage, composer clear, Send/Hold do not replay; all network denied, ephemeral auth, isolated HOME/CODEX_HOME; private tmux cleaned up")
}
