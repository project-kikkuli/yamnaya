package cli

import (
	"os"
	"os/exec"
	"path/filepath"
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
