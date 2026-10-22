package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOriginKeyIsOneNameForEveryFormOfARemote(t *testing.T) {
	for _, c := range []struct{ url, want string }{
		{"git@github.com:RajatsCode/Yamnaya-Proving-Ground.git", "github.com/rajatscode/yamnaya-proving-ground"},
		{"https://github.com/rajatscode/yamnaya-proving-ground", "github.com/rajatscode/yamnaya-proving-ground"},
		{"https://github.com/rajatscode/yamnaya-proving-ground.git/", "github.com/rajatscode/yamnaya-proving-ground"},
		{"https://token@github.com/rajatscode/yamnaya-proving-ground.git", "github.com/rajatscode/yamnaya-proving-ground"},
		{"ssh://git@github.com:22/rajatscode/yamnaya-proving-ground.git", "github.com/rajatscode/yamnaya-proving-ground"},
		{"git@gitlab.com:group/sub/app.git", "gitlab.com/group/sub/app"},
		{"/srv/git/app.git", ""},
		{"../app", ""},
		{"file:///srv/git/app.git", ""},
		{"", ""},
	} {
		if got := OriginKey(c.url); got != c.want {
			t.Errorf("OriginKey(%q) = %q, want %q", c.url, got, c.want)
		}
	}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// clone makes a repository at root/dir with origin set to url, or none when url is "", and a worktree beside it per name in wts.
func clone(t *testing.T, root, dir, url string, wts ...string) string {
	t.Helper()
	path := filepath.Join(root, dir)
	git(t, root, "init", "-q", "-b", "main", path)
	git(t, path, "commit", "-q", "--allow-empty", "-m", "start")
	if url != "" {
		git(t, path, "remote", "add", "origin", url)
	}
	for _, wt := range wts {
		git(t, path, "worktree", "add", "-q", "-b", wt, path+"-"+wt)
	}
	return path
}

type dormantOnly struct {
	Launches
	dormant []Dormant
}

func (d *dormantOnly) Dormant() []Dormant    { return d.dormant }
func (d *dormantOnly) Launched() []Launched { return nil }

func TestClonesOfOneRepositoryShareOneRepoRowAcrossDirectoriesAndHosts(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	const pg = "github.com/rajatscode/yamnaya-proving-ground"
	laptop := clone(t, root, "a/proving-ground", "git@github.com:rajatscode/yamnaya-proving-ground.git", "fix")
	other := clone(t, root, "b/yamnaya-proving-ground", "https://github.com/RajatsCode/yamnaya-proving-ground", "docs")
	local := clone(t, root, "c/proving-ground", "")

	w := newWorld(t)
	w.core.Git = ReadGit
	sessions := []struct {
		id, cwd, row string
	}{
		{"clone-main", laptop, pg},
		{"clone-worktree", laptop + "-fix", pg},
		{"https-clone", filepath.Join(other, "sub"), pg},
		{"https-worktree", other + "-docs", pg},
		{"no-remote", local, filepath.Join(local, ".git")},
	}
	must(t, os.MkdirAll(filepath.Join(other, "sub"), 0o755))
	for i, s := range sessions {
		w.run(10+i, 1, "/dev/t"+s.id, "claude", s.id, Busy)
		got := w.claude.sessions[10+i]
		got.Cwd = s.cwd
		w.claude.sessions[10+i] = got
	}
	w.core.Launches = &dormantOnly{dormant: []Dormant{{Key: "old", Harness: "claude", Session: "exited-clone", Cwd: other, RepoKey: filepath.Join(other, ".git")}}}
	w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", At: w.core.now(), Sessions: []Live{
		{Session: Session{Harness: "claude", ID: "workstation-clone", PID: 5, Status: Busy, Repo: "yamnaya-proving-ground", RepoKey: "box//home/r/pg/.git", RepoID: pg}, Project: "workstation-clone", Remote: "box"},
		{Session: Session{Harness: "claude", ID: "workstation-scratch", PID: 6, Status: Busy, Repo: "scratch", RepoKey: "box//home/r/scratch/.git", RepoID: "box//home/r/scratch/.git"}, Project: "workstation-scratch", Remote: "box"},
	}}}}
	w.core.ShowRemotes = true

	b := w.board(t)
	rowOf := map[string]string{}
	for _, r := range b.Repos {
		for _, p := range r.Missions {
			rowOf[p.Key] = r.Key
		}
	}
	want := map[string]string{"exited-clone": pg, "workstation-clone": pg, "workstation-scratch": "box//home/r/scratch/.git"}
	for _, s := range sessions {
		want[s.id] = s.row
	}
	for id, row := range want {
		if rowOf[id] != row {
			t.Errorf("%s is in repo row %q, want %q", id, rowOf[id], row)
		}
	}
	r, ok := b.Repo(pg)
	if !ok {
		t.Fatalf("no row for %s in %+v", pg, b.Repos)
	}
	if r.Name != "yamnaya-proving-ground" || len(r.Missions) != 6 {
		t.Errorf("row %q holds %d missions, want yamnaya-proving-ground with 6", r.Name, len(r.Missions))
	}
	if r.Dir != filepath.Join(laptop, ".git") && r.Dir != filepath.Join(other, ".git") {
		t.Errorf("row dir %q, want a clone on this host", r.Dir)
	}
	var hosts []string
	for _, p := range r.Missions {
		for _, s := range p.Sessions {
			if s.Remote != "" {
				hosts = append(hosts, s.Remote)
			}
		}
	}
	if !slices.Equal(hosts, []string{"box"}) {
		t.Errorf("hosts under the row %v, want the workstation's clone shown on box", hosts)
	}
	if got := len(b.Repos); got != 3 {
		t.Errorf("%d repo rows, want 3: the shared origin, the clone without a remote, the workstation's scratch", got)
	}
}

func TestARepositoryNameResolvesAcrossItsClonesToTheMostRecentlyPicked(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	laptop := clone(t, root, "a/proving-ground", "git@github.com:rajatscode/yamnaya-proving-ground.git")
	other := clone(t, root, "b/yamnaya-proving-ground", "https://github.com/rajatscode/yamnaya-proving-ground")
	c := repoCore(t, root)
	if got, err := c.ResolveRepo("yamnaya-proving-ground"); err != nil || got != laptop {
		t.Errorf("ResolveRepo before any pick = %q, %v; want the first clone by name %q", got, err, laptop)
	}
	must(t, c.Store.PickRepo(other, time.Now()))
	for _, ref := range []string{"proving-ground", "yamnaya-proving-ground", "yamnaya-prov"} {
		if got, err := c.ResolveRepo(ref); err != nil || got != other {
			t.Errorf("ResolveRepo(%q) = %q, %v; want the recently picked clone %q", ref, got, err, other)
		}
	}
	clone(t, root, "c/proving-ground", "")
	if got, err := c.ResolveRepo("proving-ground"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Errorf("ResolveRepo with a second repository named proving-ground = %q, %v; want it ambiguous", got, err)
	}
}
