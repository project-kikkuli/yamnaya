package signals

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/store"
)

func screen(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePermissionFromRealDialogs(t *testing.T) {
	cases := []struct {
		fixture string
		want    Permission
	}{
		{"claude-bash", Permission{Tool: "Bash", Detail: "rm -rf build/ && mkdir build"}},
		{"claude-write", Permission{Tool: "Write", Detail: "notes.txt"}},
		{"claude-trust", Permission{Tool: "trust folder", Detail: "/private/tmp/ymperm.yY4Y"}},
		{"codex-exec", Permission{Tool: "Shell", Detail: "rm -rf build && mkdir build"}},
		{"codex-patch", Permission{Tool: "Edit", Detail: "/Users/me/tmp/ymperm-codex/notes.txt"}},
		{"codex-trust", Permission{Tool: "trust folder", Detail: "/Users/me/tmp/ymperm-codex"}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			got, ok := ParsePermission(screen(t, c.fixture))
			if !ok || got != c.want {
				t.Fatalf("ParsePermission = %+v, %v; want %+v", got, ok, c.want)
			}
		})
	}
}

func TestParsePermissionIgnoresScreensWithoutLiveDialog(t *testing.T) {
	cases := map[string]string{
		"empty":                   "",
		"working":                 "❯ fix the tests\n⏺ Running the suite\n  ⎿  ok\n",
		"dialog text scrolled up": screen(t, "claude-bash") + strings.Repeat("⏺ later output\n", footerLines+1),
		"codex prompt quoted":     "› Would you like to run the following command?\n• sure\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			if p, ok := ParsePermission(text); ok {
				t.Fatalf("found %+v", p)
			}
		})
	}
}

// showDialog replaces target's process with a fake harness that prints a captured dialog and waits.
func showDialog(t *testing.T, socket, target, name string) {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tmux", "-L", socket, "respawn-pane", "-k", "-t", target, "sh", "-c", "cat '"+path+"'; exec sleep 600").CombinedOutput(); err != nil {
		t.Fatalf("respawn: %v\n%s", err, out)
	}
}

func TestSnapshotFlagsWindowsWaitingOnPermission(t *testing.T) {
	f := newFixture(t)
	f.project("alpha", store.Window{Name: "cl", Harness: "claude"}, store.Window{Name: "cx", Harness: "codex"}, store.Window{Name: "calm", Harness: "claude"})
	f.item("alpha", "cl")
	snap := f.snapshot()
	showDialog(t, f.tmux.Socket, f.window(snap, "alpha", "cl").TmuxTarget, "claude-bash")
	showDialog(t, f.tmux.Socket, f.window(snap, "alpha", "cx").TmuxTarget, "codex-exec")
	want := map[string]string{
		"cl":   "needs permission · Bash: rm -rf build/ && mkdir build",
		"cx":   "needs permission · Shell: rm -rf build && mkdir build",
		"calm": "running",
	}
	var got map[string]string
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		snap = f.snapshot()
		got = map[string]string{}
		for name := range want {
			got[name] = f.window(snap, "alpha", name).Label(time.Now())
		}
		if fmt.Sprint(got) == fmt.Sprint(want) {
			break
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
	if p, _ := snap.Project("alpha"); p.NeedPermission() != 2 {
		t.Errorf("NeedPermission = %d, want 2", p.NeedPermission())
	}
}
