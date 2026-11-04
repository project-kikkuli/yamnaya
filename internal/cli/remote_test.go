package cli

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRemoteCommandRunsYmWithItsDirOnPath(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncommand -v ym\nfor a in \"$@\"; do printf '[%s]\\n' \"$a\"; done\n"
	if err := os.WriteFile(filepath.Join(bin, "ym"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"sessions"}, "[sessions]\n"},
		{[]string{"profiles", "login", "codex", "personal", "--device-auth"}, "[profiles]\n[login]\n[codex]\n[personal]\n[--device-auth]\n"},
		{[]string{"item", "add", "it's a $HOME `x`"}, "[item]\n[add]\n[it's a $HOME `x`]\n"},
	} {
		c := exec.Command("sh", "-c", remoteCommand(tc.args))
		c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%v: %v: %s", tc.args, err, out)
		}
		got := string(out)
		if want := filepath.Join(bin, "ym") + "\n" + tc.want; got != want {
			t.Errorf("%v: got %q, want %q", tc.args, got, want)
		}
	}
}

func TestAttachCommandRunsYmOnAnXtermWhateverTerminalThisEndHas(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "ym"), []byte("#!/bin/sh\necho \"$TERM $*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, term := range []string{"xterm-256color", "screen-256color", "tmux-256color", "dumb"} {
		c := exec.Command("sh", "-c", attachCommand([]string{"open", "--no-board", "a b"}))
		c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "TERM=" + term}
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v: %s", term, err, out)
		}
		if got, want := string(out), "xterm-256color open --no-board a b\n"; got != want {
			t.Errorf("TERM=%s: remote ym got %q, want %q", term, got, want)
		}
	}
}

// TestYmRemoteRemembersOnlyAHostSshReachedAndLeavesItsNicknameRouting runs ym remote as a process, its ssh a script that answers as
// ssh does: usage and 255 for an @name, what the case says for any other. A closed pipe is what ym remote @x --help | head -0 leaves
// ssh writing to.
func TestYmRemoteRemembersOnlyAHostSshReachedAndLeavesItsNicknameRouting(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const printHelp, remoteFails, cannotConnect = "echo help", "echo no >&2; exit 3", "echo refused >&2; exit 255"
	for _, tc := range []struct {
		name string
		// ref is what ym remote is given, with @nick standing for the nickname of the host known only through relays.
		ref, answer string
		closedPipe  bool
		// sshed is the host ssh is asked for and remembered the host ym remembers after; "relayed" stands for the host behind @nick.
		sshed, remembered string
		wantExit          int
	}{
		{name: "a nickname's --help into a closed pipe", ref: "@nick", answer: printHelp, closedPipe: true, sshed: "relayed", wantExit: 255},
		{name: "a nickname ssh reaches", ref: "@nick", answer: printHelp, sshed: "relayed", remembered: "relayed"},
		{name: "a nickname ssh cannot reach", ref: "@nick", answer: cannotConnect, sshed: "relayed", wantExit: 255},
		{name: "a nickname no host answers to", ref: "@nowhere", answer: printHelp, wantExit: 1},
		{name: "a nickname no host answers to, into a closed pipe", ref: "@nowhere", answer: printHelp, closedPipe: true, wantExit: 1},
		{name: "a name whose ssh dies on a signal", ref: "box.example", answer: printHelp, closedPipe: true, sshed: "box.example", wantExit: 255},
		{name: "a name reached whose command fails", ref: "box.example", answer: remoteFails, sshed: "box.example", remembered: "box.example", wantExit: 3},
		{name: "a name ssh cannot reach", ref: "box.example", answer: cannotConnect, sshed: "box.example", wantExit: 255},
	} {
		t.Run(tc.name, func(t *testing.T) {
			courier, b := relayHost(t, t.TempDir()), relayHost(t, t.TempDir())
			if err := courier.SyncRelays([]string{"b"}, relayNet{"b": b}); err != nil {
				t.Fatal(err)
			}
			relayed := courier.SelfName()
			nickname := nick(t, b, relayed)
			named := func(s string) string {
				return strings.NewReplacer("relayed", relayed, "@nick", nickname).Replace(s)
			}

			dir := t.TempDir()
			called := filepath.Join(dir, "called")
			ssh := filepath.Join(dir, "ssh")
			script := `#!/bin/sh
case " $* " in *" -O "*) exit 0;; esac
eval host=\${$(($#-1))}
echo "$host" >> "` + called + `"
case "$host" in @*) echo "usage: ssh" >&2; exit 255;; esac
` + tc.answer + "\n"
			if err := os.WriteFile(ssh, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			ym := func(args ...string) *exec.Cmd {
				c := exec.Command(self, args...)
				c.Env = append(os.Environ(), testRun+"=1", "YAMNAYA_SSH_BIN="+ssh, "YAMNAYA_STATE_DIR="+b.StateDir, "YAMNAYA_NO_SERVE=1")
				return c
			}

			remote := ym("remote", named(tc.ref), "--help")
			if tc.closedPipe {
				r, w, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				r.Close()
				defer w.Close()
				remote.Stdout, remote.Stderr = w, w
			}
			err := remote.Run()
			exit := 0
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exit = ee.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if exit != tc.wantExit {
				t.Errorf("ym remote exited %d, want %d", exit, tc.wantExit)
			}

			raw, err := os.ReadFile(called)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if got, want := strings.TrimSpace(string(raw)), named(tc.sshed); got != want {
				t.Errorf("ssh was asked for %q, want %q", got, want)
			}
			known, err := b.Store.KnownHosts()
			if err != nil {
				t.Fatal(err)
			}
			var hosts []string
			for _, h := range known {
				hosts = append(hosts, h.Name)
			}
			var want []string
			if tc.remembered != "" {
				want = []string{named(tc.remembered)}
			}
			if !slices.Equal(hosts, want) {
				t.Errorf("ym remembers %q, want %q", hosts, want)
			}

			for _, args := range [][]string{
				{"goal", "send", nickname + "/mgr51", "hello"},
				{"goal", "--host", nickname, "--repo", "app", "ship the fix"},
			} {
				if o, err := ym(args...).CombinedOutput(); err != nil {
					t.Fatalf("ym %v: %v: %s", args, err, o)
				}
			}
			pending, err := b.Store.PendingRelays()
			if err != nil {
				t.Fatal(err)
			}
			// A host ym remote reached takes both over ssh rather than through the queue.
			if tc.remembered != "relayed" && len(pending) != 2 {
				t.Fatalf("queued %+v, want the send and the goal", pending)
			}
			for _, r := range pending {
				if r.Dest != relayed {
					t.Errorf("relay %s queued for %q, want %q", r.Body, r.Dest, relayed)
				}
			}
			if raw, err = os.ReadFile(called); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			if strings.Contains("\n"+string(raw), "\n@") {
				t.Errorf("ssh was asked for a nickname: %q", raw)
			}
		})
	}
}
