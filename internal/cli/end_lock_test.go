package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

// endHelper makes a copy of the test binary act as a separate ym process on the state its base names.
const endHelper = "YM_CLI_END_HELPER"

func helperLauncher(base, socket string) launcher {
	return launcher{srv: &tmux.Host{Socket: socket, Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(base, "launches")},
		conf: filepath.Join(base, "tmux.conf"), landings: filepath.Join(base, "landings.jsonl")}
}

func runEndHelper() {
	l := helperLauncher(os.Getenv("YM_BASE"), os.Getenv("YM_SOCK"))
	key := os.Getenv("YM_KEY")
	for at := os.Getenv("YM_START"); at != ""; time.Sleep(time.Millisecond) {
		if _, err := os.Stat(at); err == nil {
			break
		}
	}
	switch os.Getenv(endHelper) {
	case "kill":
		msg, err := l.End(key, "")
		fmt.Printf("%s\n%v\n", msg, err)
	case "sweep":
		if err := l.Sweep(core.Stopped(l.Launched())); err != nil {
			fmt.Println(err)
		}
	case "hold":
		if _, free, err := l.tryEnd(key); !free || err != nil {
			os.Exit(3)
		}
		fmt.Println("holding")
		time.Sleep(time.Minute)
	}
}

func endProcess(t *testing.T, l launcher, base, key, role, start string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), endHelper+"="+role, "YM_BASE="+base, "YM_SOCK="+l.srv.Socket, "YM_KEY="+key, "YM_START="+start)
	return cmd
}

// bundlesOnDisk are every bundle under the recovery directory.
func bundlesOnDisk(base string) []string {
	var out []string
	filepath.WalkDir(filepath.Join(base, "recovery"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(p, ".bundle") {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestAKillAndServeRacingOnOneRecordEndItOnce(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, wt string)
		stays bool
		files int
	}{
		{"real work beside an earlier bundle", func(t *testing.T, wt string) { plant(t, wt, "notes.txt", "half done") }, true, 1},
		{"a clean worktree", func(t *testing.T, wt string) { os.Remove(filepath.Join(wt, ".env")) }, false, 0},
	} {
		for round := range 4 {
			t.Run(fmt.Sprintf("%s/%d", c.name, round), func(t *testing.T) {
				fakeAgent(t)
				base, _ := filepath.EvalSymlinks(t.TempDir())
				l := testLauncher(t, base)
				rec, _, wt, _ := keptBundle(t, base, "r-wt-w3", "w3", l)
				c.setup(t, wt)
				now, _ := l.records.Find(rec.Key)
				now.Killed = time.Now().Add(-48 * time.Hour)
				if err := l.records.Save(now); err != nil {
					t.Fatal(err)
				}
				start := filepath.Join(base, "go")
				var outs [3][]byte
				var errs [3]error
				var wg sync.WaitGroup
				for i, role := range []string{"kill", "sweep", "kill"} {
					cmd := endProcess(t, l, base, rec.Key, role, start)
					wg.Go(func() { outs[i], errs[i] = cmd.CombinedOutput() })
				}
				time.Sleep(50 * time.Millisecond)
				os.WriteFile(start, nil, 0o644)
				wg.Wait()
				for i := range outs {
					if errs[i] != nil {
						t.Fatalf("process %d: %v: %s", i, errs[i], outs[i])
					}
					if !c.stays && strings.Contains(string(outs[i]), core.WorktreeStaysAt) {
						t.Errorf("process %d said a removed worktree stayed: %s", i, outs[i])
					}
				}
				after, _ := l.records.Find(rec.Key)
				if !after.IsLanded() {
					t.Fatalf("not landed: %+v", after)
				}
				landings, _ := os.ReadFile(l.landings)
				if n := strings.Count(string(landings), `"`+rec.Key+`"`) - 1; n != 1 {
					t.Errorf("landed %d times after the first revive's: %s", n, landings)
				}
				named := map[string]bool{}
				if after.Recovery.Bundle != "" {
					named[after.Recovery.Bundle] = true
				}
				for _, r := range after.Shelved {
					named[r.Bundle] = true
				}
				for _, b := range bundlesOnDisk(base) {
					if !named[b] {
						t.Errorf("%s holds work and nothing names it; record names %v", b, named)
					}
				}
				if after.Recovery.Files != c.files || len(after.Shelved) != 1 {
					t.Errorf("recovery %+v, shelved %+v; want %d files and the one earlier bundle", after.Recovery, after.Shelved, c.files)
				}
				if _, err := os.Stat(wt); (err == nil) != c.stays {
					t.Errorf("worktree on disk %v, want %v", err == nil, c.stays)
				}
			})
		}
	}
}

func TestServeSkipsARecordAnotherProcessIsEndingAndADeadHolderReleasesIt(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	wt := launch.WorktreePath(repo, "task")
	git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
	l := testLauncher(t, base)
	rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now().Add(-72 * time.Hour),
		Killed: time.Now().Add(-48 * time.Hour), Repo: "repo", Head: launch.Head(wt),
		Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	holder := endProcess(t, l, base, rec.Key, "hold", "")
	out, err := holder.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { holder.Process.Kill(); holder.Wait() })
	line := make([]byte, 8)
	if _, err := out.Read(line); err != nil || !strings.HasPrefix(string(line), "holding") {
		t.Fatalf("the holder said %q, %v", line, err)
	}

	started := time.Now()
	if err := l.Sweep(core.Stopped(l.Launched())); err != nil {
		t.Fatal(err)
	}
	if took := time.Since(started); took > 30*time.Second {
		t.Errorf("serve's pass took %s behind a held lock", took)
	}
	if after, _ := l.records.Find(rec.Key); after.IsLanded() {
		t.Fatal("cleaned up a record another process holds")
	}

	was := endWait
	endWait = 200 * time.Millisecond
	t.Cleanup(func() { endWait = was })
	if _, err := l.End(rec.Key, ""); err == nil || !strings.Contains(err.Error(), "still ending") {
		t.Fatalf("a kill past its wait said %v", err)
	}
	if _, err := l.Revive(rec.Key); err == nil || !strings.Contains(err.Error(), "still ending") {
		t.Fatalf("a revive past its wait said %v", err)
	}

	holder.Process.Signal(syscall.SIGKILL)
	holder.Wait()
	endWait = was
	if _, err := l.End(rec.Key, ""); err != nil {
		t.Fatal(err)
	}
	if after, _ := l.records.Find(rec.Key); !after.IsLanded() {
		t.Fatal("a dead holder kept the record locked")
	}
}
