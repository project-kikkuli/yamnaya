package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWireAnExitedSessionShowsItsConversationCannotBeRead(t *testing.T) {
	w := newWireWorld(t)
	path := filepath.Join(w.def.Root, "projects", strings.NewReplacer("/", "-", ".", "-").Replace(w.cwd), profileConversation+".jsonl")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	deadline := time.Now().Add(10 * time.Second)
	for {
		unreadable.mu.Lock()
		if !unreadable.running {
			unreadable.checked = time.Time{}
		}
		unreadable.mu.Unlock()
		dormant := w.l.Dormant()
		if len(dormant) == 1 && strings.Contains(dormant[0].Unreadable, "permission denied") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("dormant %+v", dormant)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
