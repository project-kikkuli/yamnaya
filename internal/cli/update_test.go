package cli

import (
	"bytes"
	"errors"
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
			running := filepath.Join(root, "local", "ym")
			writeFile(t, running, "old ym")
			var restarted []string
			cmd := updateCmd(runGo, func() (string, error) { return running, nil }, func(_ io.Writer, bin string) error {
				restarted = append(restarted, bin)
				return nil
			})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(slices.DeleteFunc([]string{tc.ref}, func(s string) bool { return s == "" }))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			for _, bin := range []string{running, filepath.Join(root, "bin", "ym")} {
				if !strings.Contains(out.String(), "installed ym "+tc.sha[:12]+" at "+bin) {
					t.Fatalf("output %q does not report %s at %s", out.String(), tc.sha[:12], bin)
				}
				got, err := exec.Command(bin).Output()
				if err != nil || string(got) != tc.says {
					t.Fatalf("%s says %q (%v), want %q", bin, got, err, tc.says)
				}
			}
			if got := readFile(t, running+".bak"); got != "old ym" {
				t.Fatalf("backup holds %q, want the old ym", got)
			}
			if !slices.Equal(restarted, []string{running}) {
				t.Fatalf("restarted into %v, want %s", restarted, running)
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
	writeFile(t, filepath.Join(root, "ym"), "old ym")
	cmd := updateCmd(func(io.Writer, ...string) error { ran = true; return nil }, func() (string, error) { return filepath.Join(root, "ym"), nil }, nil)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"no-such-branch"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "no-such-branch") {
		t.Fatalf("err = %v, want one naming the missing ref", err)
	}
	if ran {
		t.Fatal("go ran for a ref the repository lacks")
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fakeUpdate serves ym update a repository with one commit and a go that "builds" a file saying "new ym", with GOBIN under
// root, and returns that go and the restarts it records.
func fakeUpdate(t *testing.T, root string) (goRunner, *[]string) {
	t.Helper()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	origin := filepath.Join(root, "origin.git")
	for _, args := range [][]string{{"init", "-q", "-b", "main", origin}, {"-C", origin, "commit", "-q", "--allow-empty", "-m", "x"}} {
		if out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	old := ymRepo
	ymRepo = origin
	t.Cleanup(func() { ymRepo = old })
	run := func(out io.Writer, args ...string) error {
		if i := slices.Index(args, "-o"); i >= 0 {
			return os.WriteFile(args[i+1], []byte("new ym"), 0o644)
		}
		if slices.Contains(args, "env") {
			_, err := io.WriteString(out, filepath.Join(root, "gobin")+"\n\n")
			return err
		}
		return nil
	}
	return run, &[]string{}
}

func runUpdate(run goRunner, exe string, restarted *[]string) (string, error) {
	cmd := updateCmd(run, func() (string, error) { return exe, nil }, func(_ io.Writer, bin string) error {
		*restarted = append(*restarted, bin)
		return nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)
	err := cmd.Execute()
	return out.String(), err
}

func TestUpdateReplacesTheRunningYmItsSymlinkNamesAndKeepsABackup(t *testing.T) {
	root := t.TempDir()
	run, restarted := fakeUpdate(t, root)
	real := filepath.Join(root, "real", "ym")
	writeFile(t, real, "old ym")
	link := filepath.Join(root, "local", "bin", "ym")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	out, err := runUpdate(run, link, restarted)
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got := readFile(t, real); got != "new ym" {
		t.Fatalf("running ym holds %q, want the new ym", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("%s is no longer a symlink: %v", link, err)
	}
	if fi, err := os.Stat(real); err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("new ym mode %v (%v), want 755", fi.Mode(), err)
	}
	if got := readFile(t, real+".bak"); got != "old ym" {
		t.Fatalf("backup holds %q, want the old ym", got)
	}
	if _, err := os.Stat(real + ".new"); !os.IsNotExist(err) {
		t.Fatalf("%s.new left behind: %v", real, err)
	}
	if got := readFile(t, filepath.Join(root, "gobin", "ym")); got != "new ym" {
		t.Fatalf("GOBIN ym holds %q, want the new ym", got)
	}
	if !slices.Equal(*restarted, []string{real}) {
		t.Fatalf("restarted into %v, want %s", *restarted, real)
	}
}

func TestUpdateFailsWhenItCannotWriteTheRunningYm(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes any directory")
	}
	root := t.TempDir()
	run, restarted := fakeUpdate(t, root)
	running := filepath.Join(root, "locked", "ym")
	writeFile(t, running, "old ym")
	if err := os.Chmod(filepath.Dir(running), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(running), 0o755) })
	out, err := runUpdate(run, running, restarted)
	if err == nil || !strings.Contains(err.Error(), running) {
		t.Fatalf("err = %v, want one naming %s", err, running)
	}
	if strings.Contains(out, "installed") {
		t.Fatalf("output %q reports an install", out)
	}
	if got := readFile(t, running); got != "old ym" {
		t.Fatalf("running ym holds %q, want it untouched", got)
	}
	if len(*restarted) != 0 {
		t.Fatalf("restarted into %v after a failed update", *restarted)
	}
}

func TestUpdateReportsAFailedRestart(t *testing.T) {
	root := t.TempDir()
	run, _ := fakeUpdate(t, root)
	running := filepath.Join(root, "local", "ym")
	writeFile(t, running, "old ym")
	cmd := updateCmd(run, func() (string, error) { return running, nil }, func(io.Writer, string) error { return errors.New("service keep: no") })
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "service keep: no") {
		t.Fatalf("err = %v, want the restart's failure", err)
	}
}

func TestRestartIntoRunsTheNewYmAsRemoteInstallDoes(t *testing.T) {
	for _, tc := range []struct {
		name, fail string
		want       []string
		ok         bool
	}{
		{"keep works", "", []string{"install --statusline", "service keep"}, true},
		{"keep fails", "service keep", []string{"install --statusline", "service keep", "service ensure"}, true},
		{"nothing starts serve", "service", []string{"install --statusline", "service keep", "service ensure"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "log")
			bin := filepath.Join(dir, "ym")
			writeFile(t, bin, "#!/bin/sh\necho \"$*\" >> "+log+"\ncase \"$*\" in \""+tc.fail+"\"*) [ -n \""+tc.fail+"\" ] && exit 1;; esac\nexit 0\n")
			err := restartInto(io.Discard, bin)
			if (err == nil) != tc.ok {
				t.Fatalf("err = %v, want ok %v", err, tc.ok)
			}
			if got := strings.Split(strings.TrimSpace(readFile(t, log)), "\n"); !slices.Equal(got, tc.want) {
				t.Fatalf("ran %q, want %q", got, tc.want)
			}
		})
	}
}
