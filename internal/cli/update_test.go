package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestUpdateBuildsTheCommitAskedForFromItsRepository serves a module whose go.mod replaces a dependency with a directory in
// the repository, as ym's does, which go install module@version refuses to build.
func TestUpdateBuildsTheCommitAskedForFromItsRepository(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("GOBIN", filepath.Join(root, "bin"))
	t.Setenv("GOFLAGS", "")
	work := filepath.Join(root, "work")
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-C", work}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(path, text string) {
		t.Helper()
		path = filepath.Join(work, path)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	commit := func(says string) string {
		write("dep/dep.go", "package dep\n\nconst Says = \""+says+"\"\n")
		git("add", "-A")
		git("commit", "-q", "-m", says)
		return git("rev-parse", "HEAD")
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	git("init", "-q", "-b", "main")
	write("go.mod", "module example.com/ymtest\n\ngo 1.26.0\n\nrequire example.com/dep v0.0.0\n\nreplace example.com/dep => ./dep\n")
	write("dep/go.mod", "module example.com/dep\n\ngo 1.26.0\n")
	write("cmd/ym/main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/dep\"\n)\n\nfunc main() { fmt.Print(dep.Says) }\n")
	first := commit("first")
	latest := commit("latest")
	git("switch", "-q", "-c", "side")
	side := commit("side")
	git("switch", "-q", "main")
	origin := filepath.Join(root, "origin.git")
	git("clone", "-q", "--bare", work, origin)
	old := ymRepo
	ymRepo = origin
	t.Cleanup(func() { ymRepo = old })
	for _, tc := range []struct {
		name, ref, sha, says string
	}{
		{"default branch", "", latest, "latest"},
		{"an older commit", first, first, "first"},
		{"a branch", "side", side, "side"},
		{"the default branch by name", "main", latest, "latest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := updateCmd(runGo)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(slices.DeleteFunc([]string{tc.ref}, func(s string) bool { return s == "" }))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			bin := filepath.Join(root, "bin", "ym")
			if !strings.Contains(out.String(), "installed ym "+tc.sha[:12]+" at "+bin) {
				t.Fatalf("output %q does not report %s at %s", out.String(), tc.sha[:12], bin)
			}
			got, err := exec.Command(bin).Output()
			if err != nil || string(got) != tc.says {
				t.Fatalf("installed ym says %q (%v), want %q", got, err, tc.says)
			}
		})
	}
}

func TestUpdateRefusesARefTheRepositoryLacks(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	origin := filepath.Join(root, "origin.git")
	if out, err := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "init", "-q", "-b", "main", origin).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if out, err := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "-C", origin, "commit", "-q", "--allow-empty", "-m", "x").CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	old := ymRepo
	ymRepo = origin
	t.Cleanup(func() { ymRepo = old })
	var ran bool
	cmd := updateCmd(func(io.Writer, ...string) error { ran = true; return nil })
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"no-such-branch"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("err = %v, want one naming the missing ref", err)
	}
	if ran {
		t.Fatal("go ran for a ref the repository lacks")
	}
}
