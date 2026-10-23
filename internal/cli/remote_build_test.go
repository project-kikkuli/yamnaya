package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestEmbeddedSourceBuildsYmForAnotherHost(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain")
	}
	dir := t.TempDir()
	if err := buildSource(gobin, dir, "linux", "amd64"); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "ym-linux-amd64")); err != nil || fi.Size() == 0 {
		t.Fatalf("no ym built: %v", err)
	}
}
