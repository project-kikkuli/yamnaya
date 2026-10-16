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

// repoTree lays out paths under a temp dir: "x/" is a main checkout, "x=wt" a worktree, anything else a plain directory.
func repoTree(t *testing.T, paths ...string) string {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for _, p := range paths {
		switch {
		case strings.HasSuffix(p, "/"):
			must(t, os.MkdirAll(filepath.Join(root, p, ".git"), 0o755))
		case strings.HasSuffix(p, "=wt"):
			dir := filepath.Join(root, strings.TrimSuffix(p, "=wt"))
			must(t, os.MkdirAll(dir, 0o755))
			must(t, os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: "+root+"/x/.git/worktrees/y\n"), 0o644))
		default:
			must(t, os.MkdirAll(filepath.Join(root, p), 0o755))
		}
	}
	return root
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func scan(roots ...string) []string {
	ch := make(chan string)
	go ScanRepos(roots, ch)
	var out []string
	for r := range ch {
		out = append(out, r)
	}
	slices.Sort(out)
	return out
}

func rel(root string, paths []string) []string {
	var out []string
	for _, p := range paths {
		r, _ := filepath.Rel(root, p)
		out = append(out, r)
	}
	return out
}

func TestScanFindsMainCheckoutsWithinTwoLevels(t *testing.T) {
	for _, c := range []struct {
		name  string
		tree  []string
		twice bool
		want  []string
	}{
		{"repo and its worktrees", []string{"app/", "app-wt-fix=wt", "app-wt-feat=wt"}, false, []string{"app"}},
		{"grouped repos", []string{"work/api/", "work/web/", "work/api-wt-x=wt", "solo/"}, false, []string{"solo", "work/api", "work/web"}},
		{"too deep", []string{"a/b/c/", "a/b/"}, false, []string{"a/b"}},
		{"plain dirs and hidden", []string{"notes/sub", ".cache/thing/", "tool/"}, false, []string{"tool"}},
		{"root is a repo", []string{"./", "inner/"}, false, []string{".", "inner"}},
		{"root listed twice", []string{"app/"}, true, []string{"app"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := repoTree(t, c.tree...)
			roots := []string{root}
			if c.twice {
				roots = append(roots, root+"/")
			}
			if got := rel(root, scan(roots...)); !slices.Equal(got, c.want) {
				t.Fatalf("found %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchReposRanksNameMatchesFirst(t *testing.T) {
	repos := []string{"/d/atlantic-api", "/d/atlas", "/d/acme/atlantic-cli", "/d/yamnaya", "/d/app"}
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", repos},
		{"atlas", []string{"/d/atlas"}},
		{"atl", []string{"/d/atlantic-api", "/d/atlas", "/d/acme/atlantic-cli"}},
		{"cli", []string{"/d/acme/atlantic-cli"}},
		{"acme", []string{"/d/acme/atlantic-cli"}},
		{"ymn", []string{"/d/yamnaya"}},
		{"APP", []string{"/d/app"}},
		{"zzz", nil},
	} {
		if got := MatchRepos(repos, c.query); !slices.Equal(got, c.want) {
			t.Errorf("MatchRepos(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func repoCore(t *testing.T, root string) *Core {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	must(t, err)
	t.Cleanup(func() { st.Close() })
	return &Core{Store: st, Roots: func() []string { return []string{root} }}
}

func TestResolveRepoTakesAUniqueNameOrAPath(t *testing.T) {
	root := repoTree(t, "acme/atlas/", "acme/atlantic-api/", "acme/atlas-wt-x=wt", "kikkuli/yamnaya/", "one/app/", "two/app/")
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		ref  string
		want string
		err  string
	}{
		{"yamnaya", "kikkuli/yamnaya", ""},
		{"yam", "kikkuli/yamnaya", ""},
		{"atlas", "acme/atlas", ""},
		{"atl", "", "matches several"},
		{"app", "", "matches several"},
		{"nothing", "", "no repository matches"},
		{"/abs/path", "/abs/path", ""},
		{"~/src", filepath.Join(home, "src"), ""},
	} {
		t.Run(c.ref, func(t *testing.T) {
			got, err := repoCore(t, root).ResolveRepo(c.ref)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("ResolveRepo(%q) = %q, %v; want error %q", c.ref, got, err, c.err)
				}
				return
			}
			want := c.want
			if !filepath.IsAbs(want) {
				want = filepath.Join(root, want)
			}
			if err != nil || got != want {
				t.Fatalf("ResolveRepo(%q) = %q, %v; want %q", c.ref, got, err, want)
			}
		})
	}
	if _, err := repoCore(t, root).ResolveRepo("app"); err == nil || !strings.Contains(err.Error(), filepath.Join(root, "two/app")) {
		t.Fatalf("ambiguous error should list the matches: %v", err)
	}
}

type startOnly struct {
	Launches
	started []NewLaunch
}

func (s *startOnly) Start(n NewLaunch) (string, error) {
	s.started = append(s.started, n)
	return "started", nil
}

func TestStartedReposComeFirstMostRecentFirstAsTheirMainCheckout(t *testing.T) {
	root := repoTree(t, "b/", "c/")
	a := filepath.Join(root, "a")
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", a},
		{"-C", a, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"-C", a, "worktree", "add", "-q", "-b", "x", filepath.Join(root, "a-wt-x")},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	c := repoCore(t, root)
	l := &startOnly{}
	c.Launches = l
	for _, r := range []string{"c", "a-wt-x", "b"} {
		if _, err := c.StartNew(NewLaunch{Prompt: "go", Repo: filepath.Join(root, r)}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	got, err := c.Repos()
	must(t, err)
	if want := []string{"b", "a", "c"}; !slices.Equal(rel(root, got), want) {
		t.Fatalf("repos %v, want %v", rel(root, got), want)
	}
	if len(l.started) != 3 || l.started[1].Repo != filepath.Join(root, "a-wt-x") {
		t.Fatalf("started %v", l.started)
	}
}
