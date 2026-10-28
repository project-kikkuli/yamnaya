package launch

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Unmerged is how long gc trusts that a commit is in no merged pull request before asking GitHub again.
const Unmerged = 24 * time.Hour

// PR is a merged pull request, the commit its head branch ended on, the branch it merged into and the commit that
// merge made there.
type PR struct {
	Number int    `json:"number"`
	Head   string `json:"headRefOid"`
	Base   string `json:"baseRefName"`
	Merge  struct {
		Oid string `json:"oid"`
	} `json:"mergeCommit"`
}

// Hub is what GitHub says about the merged pull requests of repository owner/name.
type Hub interface {
	// Merged lists merged pull requests whose head branch is branch.
	Merged(repo, branch string) ([]PR, error)
	// Holding lists merged pull requests that contain commit sha.
	Holding(repo, sha string) ([]PR, error)
	// Ancestor reports whether sha equals or precedes head on GitHub; a sha GitHub lacks precedes nothing.
	Ancestor(repo, sha, head string) (bool, error)
}

// GH is Hub through the gh CLI.
type GH struct{}

var errMissing = errors.New("not on GitHub")

func gh(args ...string) ([]byte, error) {
	var stderr strings.Builder
	cmd := exec.Command("gh", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if strings.Contains(stderr.String(), "HTTP 404") || strings.Contains(stderr.String(), "HTTP 422") {
			return nil, errMissing
		}
		return nil, errors.New("gh " + strings.Join(args, " ") + ": " + err.Error() + ": " + strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

func prs(args ...string) ([]PR, error) {
	out, err := gh(append([]string{"pr", "list", "--state", "merged", "--json", "number,headRefOid,baseRefName,mergeCommit"}, args...)...)
	if err != nil {
		return nil, err
	}
	var all []PR
	return all, json.Unmarshal(out, &all)
}

func (GH) Merged(repo, branch string) ([]PR, error) { return prs("--repo", repo, "--head", branch) }

func (GH) Holding(repo, sha string) ([]PR, error) { return prs("--repo", repo, "--search", sha) }

func (GH) Ancestor(repo, sha, head string) (bool, error) {
	out, err := gh("api", "repos/"+repo+"/compare/"+sha+"..."+head, "--jq", ".status")
	if errors.Is(err, errMissing) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s := strings.TrimSpace(string(out))
	return s == "identical" || s == "ahead", nil
}

var github = regexp.MustCompile(`github\.com[:/]([^/]+/[^/]+?)(?:\.git)?/?$`)

// GitHubRepo is the owner/name of the GitHub repository origin points at, or "".
func GitHubRepo(root string) string {
	url, err := look(root, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	if m := github.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return ""
}

type verdict struct {
	PR int       `json:"pr"`
	At time.Time `json:"at"`
}

// Squash finds the merged pull request a commit ended up in, remembering each answer in a file so an hourly gc
// asks GitHub only about commits it has not seen, or saw in no pull request over Unmerged ago.
type Squash struct {
	Hub   Hub
	Cache string
	Now   func() time.Time

	mu    sync.Mutex
	seen  map[string]verdict
	slugs map[string]string
}

func (q *Squash) load() {
	if q.seen != nil {
		return
	}
	q.seen, q.slugs = map[string]verdict{}, map[string]string{}
	if b, err := os.ReadFile(q.Cache); err == nil {
		json.Unmarshal(b, &q.seen)
	}
}

func (q *Squash) save() error {
	b, err := json.Marshal(q.seen)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(q.Cache), 0o755); err != nil {
		return err
	}
	tmp := q.Cache + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, q.Cache)
}

// Merged is the number of a merged pull request whose head commit is sha or descends from it, or 0. branch is the
// local branch at sha, or "" when detached; its upstream branch name, else its own, names the pull request's head.
func (q *Squash) Merged(root, branch, sha string) (int, error) {
	if q == nil || sha == "" {
		return 0, nil
	}
	key := root + " " + sha
	q.mu.Lock()
	q.load()
	v, ok := q.seen[key]
	slug, known := q.slugs[root]
	q.mu.Unlock()
	if ok && (v.PR > 0 || q.Now().Sub(v.At) < Unmerged) {
		return v.PR, nil
	}
	if !known {
		slug = GitHubRepo(root)
		q.mu.Lock()
		q.slugs[root] = slug
		q.mu.Unlock()
	}
	if slug == "" {
		return 0, nil
	}
	n, err := q.ask(root, slug, branch, sha)
	if err != nil {
		return 0, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seen[key] = verdict{PR: n, At: q.Now()}
	return n, q.save()
}

func (q *Squash) ask(root, slug, branch, sha string) (int, error) {
	var found []PR
	if branch != "" {
		head := branch
		if up, err := look(root, "config", "--get", "branch."+branch+".merge"); err == nil && up != "" {
			head = strings.TrimPrefix(up, "refs/heads/")
		}
		var err error
		if found, err = q.Hub.Merged(slug, head); err != nil {
			return 0, err
		}
	}
	if len(found) == 0 {
		var err error
		if found, err = q.Hub.Holding(slug, sha); err != nil {
			return 0, err
		}
	}
	for _, pr := range found {
		if ok, err := q.contains(root, slug, pr.Head, sha); err != nil || !ok {
			if err != nil {
				return 0, err
			}
			continue
		}
		if ok, err := q.landed(root, slug, pr); err != nil || ok {
			return pr.Number, err
		}
	}
	return 0, nil
}

// contains reports whether sha equals or precedes head, locally when head is here, else on GitHub.
func (q *Squash) contains(root, slug, head, sha string) (bool, error) {
	if head == sha {
		return true, nil
	}
	if local("-C", root, "cat-file", "-e", head+"^{commit}") == nil {
		return local("-C", root, "merge-base", "--is-ancestor", sha, head) == nil, nil
	}
	return q.Hub.Ancestor(slug, sha, head)
}

// landed reports whether pr's work reached the default branch: it merged into it, or its merge commit is on it or in a
// merged pull request that landed in turn.
func (q *Squash) landed(root, slug string, pr PR) (bool, error) {
	base := Base(root)
	branch := strings.TrimPrefix(base, "origin/")
	if pr.Base == branch {
		return true, nil
	}
	if pr.Merge.Oid == "" || base == "" {
		return false, nil
	}
	if local("-C", root, "cat-file", "-e", pr.Merge.Oid+"^{commit}") == nil {
		if _, err := look(root, "merge-base", "--is-ancestor", pr.Merge.Oid, base); err == nil {
			return true, nil
		}
	} else if ok, err := q.Hub.Ancestor(slug, pr.Merge.Oid, branch); err != nil || ok {
		return ok, err
	}
	n, err := q.ask(root, slug, pr.Base, pr.Merge.Oid)
	return n > 0, err
}
