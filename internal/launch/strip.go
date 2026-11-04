package launch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// regenerableDirs and regenerableFiles name the git-ignored entries a build or install recreates. A directory counts only
// when git itself reports the whole directory as ignored, so a stray file under a like-named tracked directory never does.
var regenerableDirs = map[string]bool{
	"node_modules": true, ".venv": true, "target": true, "__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".next": true, ".turbo": true, ".parcel-cache": true, ".gradle": true, ".tox": true, ".nox": true,
	".svelte-kit": true,
}

var regenerableFiles = []string{"*.pyc", "*.pyo", "*.tsbuildinfo", ".DS_Store", ".eslintcache"}

// targetBuildFiles are the build files whose being tracked beside it makes a target directory a build's output: unlike the rest
// of the allowlist, which only its tool writes to, target is a name anyone might use for a folder.
var targetBuildFiles = []string{"Cargo.toml", "pom.xml"}

// checkoutFiles name what marks a checkout and may be a file: a linked git worktree's .git, and Fossil's checkout database.
var checkoutFiles = []string{".git", ".fslckout", "_FOSSIL_"}

// checkoutDirs name what marks a checkout only as a directory.
var checkoutDirs = []string{".hg", ".jj", ".svn", ".bzr", "_darcs", ".pijul", ".sl", "CVS"}

// holdsRepo reports whether anything at any depth under path is a repository: a version control directory, or a bare git
// repository's HEAD beside its objects and refs. What cannot be read counts as one.
func holdsRepo(path string) bool {
	found := false
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil || slices.Contains(checkoutFiles, d.Name()) || d.IsDir() && slices.Contains(checkoutDirs, d.Name()) ||
			d.Name() == "HEAD" && isBareRepo(filepath.Dir(p)) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func isBareRepo(dir string) bool {
	for _, sub := range []string{"objects", "refs"} {
		if fi, err := os.Stat(filepath.Join(dir, sub)); err != nil || !fi.IsDir() {
			return false
		}
	}
	return true
}

// ignoredEntries lists the git-ignored entries of the worktree at path as git reports them, folding a virtualenv that
// ignores itself, as uv's do, back into one directory.
func ignoredEntries(path string) ([]string, error) {
	out, err := look(path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	var all []string
	venvs := map[string]bool{}
	for _, e := range strings.FieldsFunc(out, func(r rune) bool { return r == 0 }) {
		if venv, ok := selfIgnoringVenv(path, e); ok {
			if !venvs[venv] {
				all = append(all, venv)
			}
			venvs[venv] = true
			continue
		}
		all = append(all, e)
	}
	return all, nil
}

func selfIgnoringVenv(root, entry string) (string, bool) {
	dir, _, ok := strings.Cut("/"+entry, "/.venv/")
	if !ok || strings.TrimSuffix(entry, "/") == strings.TrimPrefix(dir+"/.venv", "/") {
		return "", false
	}
	venv := strings.TrimPrefix(dir+"/.venv/", "/")
	if _, err := os.Stat(filepath.Join(root, venv, "pyvenv.cfg")); err != nil {
		return "", false
	}
	if rule, err := os.ReadFile(filepath.Join(root, venv, ".gitignore")); err != nil || strings.TrimSpace(string(rule)) != "*" {
		return "", false
	}
	if tracked, err := look(root, "--literal-pathspecs", "ls-files", "--", venv); err != nil || tracked != "" {
		return "", false
	}
	return venv, true
}

// regenerable reports whether an ignored entry of the worktree at root is safe to delete: an allowlisted whole directory
// holding no other repository or worktree, or an allowlisted file.
func regenerable(root, entry string) bool {
	base := filepath.Base(strings.TrimSuffix(entry, "/"))
	if !strings.HasSuffix(entry, "/") {
		return slices.ContainsFunc(regenerableFiles, func(p string) bool {
			ok, _ := filepath.Match(p, base)
			return ok
		})
	}
	if !regenerableDirs[base] {
		return false
	}
	if base == "target" && !trackedBuildFile(root, filepath.Dir(strings.TrimSuffix(entry, "/"))) {
		return false
	}
	return !holdsRepo(filepath.Join(root, entry))
}

// trackedBuildFile reports whether HEAD holds one of targetBuildFiles as a file in dir; staged, a directory or a link do not count.
func trackedBuildFile(root, dir string) bool {
	for _, f := range targetBuildFiles {
		out, err := look(root, "--literal-pathspecs", "ls-tree", "HEAD", "--", filepath.Join(dir, f))
		if err == nil && (strings.HasPrefix(out, "100644 blob ") || strings.HasPrefix(out, "100755 blob ")) {
			return true
		}
	}
	return false
}

// keepContents says why what lies in the worktree at path beyond its commits must stay: a repository nested at any depth under
// an ignored directory always, with strict anything ignored off the allowlist, and a submodule not proven to lose nothing.
// Removing a worktree takes its ignored files and its submodules' git dirs with it. forced says it has submodules, all proven.
func keepContents(path string, strict bool) (keep, why string, forced bool) {
	if hidden, err := hiddenEdits(path); err != nil {
		return KeepFailing, err.Error(), false
	} else if hidden != "" {
		return KeepDirty, "a flag hides any edit to " + hidden + " from git status", false
	}
	links, err := gitlinks(path)
	if err != nil {
		return KeepFailing, err.Error(), false
	}
	// --force takes everything ignored with it, so forcing always takes the strict rule
	strict = strict || len(links) > 0
	all, err := ignoredEntries(path)
	if err != nil {
		return KeepFailing, err.Error(), false
	}
	for _, e := range all {
		if strings.HasSuffix(e, "/") && holdsRepo(filepath.Join(path, e)) {
			return KeepNested, "holds a repository under " + e, false
		}
	}
	if strict {
		for _, e := range all {
			if !regenerable(path, e) {
				return KeepIgnored, "holds git-ignored " + e, false
			}
		}
	}
	proven := map[string]bool{}
	for sub, recorded := range links {
		if why := proveSubmodule(filepath.Join(path, sub), recorded, proven); why != "" {
			return KeepModules, "submodule " + sub + " " + why, false
		}
	}
	if stray, err := strayModule(path, proven); err != nil {
		return KeepFailing, err.Error(), false
	} else if stray != "" {
		return KeepModules, "holds a submodule git dir no submodule uses: " + stray, false
	}
	return "", "", len(links) > 0
}

// gitlinks maps the submodules the index of the worktree at path records to their commits.
func gitlinks(path string) (map[string]string, error) {
	stage, err := look(path, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	links := map[string]string{}
	for _, line := range strings.FieldsFunc(stage, func(r rune) bool { return r == 0 }) {
		meta, name, ok := strings.Cut(line, "\t")
		f := strings.Fields(meta)
		if ok && len(f) == 3 && f[0] == "160000" {
			links[name] = f[1]
		}
	}
	return links, nil
}

// hiddenEdits names a tracked file git status cannot vouch for, because --assume-unchanged or --skip-worktree is set on it, or "".
func hiddenEdits(path string) (string, error) {
	out, err := look(path, "ls-files", "-v", "-z")
	if err != nil {
		return "", err
	}
	for _, line := range strings.FieldsFunc(out, func(r rune) bool { return r == 0 }) {
		if flag, name, ok := strings.Cut(line, " "); ok && flag != "" && (flag == "S" || flag[0] >= 'a' && flag[0] <= 'z') {
			return name, nil
		}
	}
	return "", nil
}

// remoteLimit bounds asking one remote what it holds.
const remoteLimit = time.Minute

// remoteRefs asks every remote of the repository at path what it holds now, ref by ref, since a local remote-tracking ref may
// outlive the branch it names. It only reads, so a worktree it keeps stays exactly as it was. No remote or any failure is an error.
func remoteRefs(path string) (map[string]string, error) {
	remotes, err := look(path, "remote")
	if err != nil {
		return nil, err
	}
	names := strings.Fields(remotes)
	if len(names) == 0 {
		return nil, errors.New("no remote")
	}
	refs := map[string]string{}
	for _, name := range names {
		ctx, cancel := context.WithTimeout(context.Background(), remoteLimit)
		cmd := exec.CommandContext(ctx, "git", "-C", path, "ls-remote", name)
		cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
		cmd.WaitDelay = 5 * time.Second
		out, err := cmd.Output()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("git ls-remote %s: %w", name, err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if sha, ref, ok := strings.Cut(line, "\t"); ok {
				refs[ref] = sha
			}
		}
	}
	return refs, nil
}

// unprovenCommits says what, in the repository at path, is on no remote per refs: every local ref but remote-tracking ones, HEAD,
// and everything in the reflogs of HEAD and the branches. A tip this repository lacks proves nothing and is left out.
func unprovenCommits(path string, refs map[string]string) (string, error) {
	var check strings.Builder
	for _, sha := range refs {
		check.WriteString(sha + "\n")
	}
	have, err := gitEnv(path, nil, []byte(check.String()), "cat-file", "--batch-check=%(objectname) %(objecttype)")
	if err != nil {
		return "", err
	}
	var revs strings.Builder
	for _, line := range strings.Split(have, "\n") {
		if sha, kind, ok := strings.Cut(line, " "); ok && kind != "missing" {
			revs.WriteString("^" + sha + "\n")
		}
	}
	logged, err := look(path, "log", "--walk-reflogs", "--format=%H", "HEAD", "--branches")
	if err != nil {
		return "", err
	}
	revs.WriteString(logged + "\n")
	count, err := gitEnv(path, nil, []byte(revs.String()), "rev-list", "--count", "--stdin", "--exclude=refs/remotes/*", "--all", "HEAD")
	if err != nil {
		return "", err
	}
	if count != "0" {
		return count + " commits on no remote's current refs, counting HEAD, branches, tags and reflogs", nil
	}
	return "", nil
}

// proveSubmodule says why removing the submodule checked out at path could lose something, or "". Its git dir and objects go with
// the worktree and exist nowhere else, so everything in them must be on a remote as the remote says now.
func proveSubmodule(path, recorded string, proven map[string]bool) string {
	if _, err := os.Stat(filepath.Join(path, ".git")); err != nil {
		entries, err := os.ReadDir(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) || len(entries) > 0 {
			return "has files but no git dir"
		}
		return ""
	}
	head, err := look(path, "rev-parse", "HEAD")
	if err != nil {
		return "cannot be read: " + err.Error()
	}
	if head != recorded {
		return "is at " + head + ", not the commit its parent records"
	}
	status, err := look(path, "status", "--porcelain", "--untracked-files=normal", "--ignore-submodules=none")
	if err != nil {
		return "cannot be read: " + err.Error()
	}
	if status != "" {
		return "is dirty"
	}
	stash, err := look(path, "stash", "list")
	if err != nil {
		return "cannot be read: " + err.Error()
	}
	if stash != "" {
		return "has a stash"
	}
	gitDir, err := look(path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "cannot be read: " + err.Error()
	}
	if linked, err := os.ReadDir(filepath.Join(gitDir, "worktrees")); err != nil && !errors.Is(err, fs.ErrNotExist) || len(linked) > 0 {
		return "has linked worktrees of its own"
	}
	if keep, why, _ := keepContents(path, true); keep != "" {
		return "has " + keep + ": " + why
	}
	refs, err := remoteRefs(path)
	if err != nil {
		return "cannot be checked against its remote: " + err.Error()
	}
	tags, err := look(path, "for-each-ref", "--format=%(refname) %(objectname)", "refs/tags")
	if err != nil {
		return "cannot be read: " + err.Error()
	}
	for _, line := range strings.Split(tags, "\n") {
		if ref, sha, ok := strings.Cut(line, " "); ok && refs[ref] != sha {
			return "has tag " + ref + " its remote does not"
		}
	}
	unproven, err := unprovenCommits(path, refs)
	if err != nil {
		return "cannot be checked against its remote: " + err.Error()
	}
	if unproven != "" {
		return "has " + unproven
	}
	proven[gitDir] = true
	return ""
}

// strayModule is a git dir under the modules of the worktree at path that no proven submodule uses, or "".
func strayModule(path string, proven map[string]bool) (string, error) {
	admin, err := look(path, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	stray := ""
	filepath.WalkDir(filepath.Join(admin, "modules"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				stray = p
				return filepath.SkipAll
			}
			return nil
		}
		if !d.IsDir() || !isBareRepo(p) {
			return nil
		}
		if !proven[p] {
			stray = p
			return filepath.SkipAll
		}
		return filepath.SkipDir
	})
	return stray, nil
}

// Stripped is what a strip took out of one worktree: how many entries of each name, and their bytes when measured.
type Stripped struct {
	Sweep   Sweep
	Entries map[string]int
	Size    int64
	Err     error
}

// Why is one line naming what was stripped, biggest count first.
func (s Stripped) Why() string {
	names := slices.Collect(maps.Keys(s.Entries))
	sort.Slice(names, func(i, j int) bool {
		if s.Entries[names[i]] != s.Entries[names[j]] {
			return s.Entries[names[i]] > s.Entries[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s x%d", n, s.Entries[n])
	}
	return strings.Join(parts, ", ")
}

// StripIdle is how long a kept worktree goes untouched before a reclaim pass strips it.
const StripIdle = 6 * time.Hour

// Strippable reports whether a worktree gc decided on may lose its regenerable files: gc kept it, it went StripIdle
// untouched and no process works in it. Locked and unreadable worktrees are never touched.
func Strippable(w Sweep) bool {
	return w.Keep != "" && w.Keep != KeepLocked && w.Keep != KeepFailing && w.Keep != KeepInUse && w.User == "" && w.Idle >= StripIdle
}

// StripIgnored removes the allowlisted git-ignored entries of the worktree w, confirming each with git check-ignore, and leaves
// the worktree's own mtime as it was so the strip does not count as a touch. With dry it only measures.
func StripIgnored(w Sweep, dry bool) Stripped {
	out := Stripped{Sweep: w, Entries: map[string]int{}}
	all, err := ignoredEntries(w.Path)
	if err != nil {
		out.Err = err
		return out
	}
	all = slices.DeleteFunc(all, func(e string) bool { return !regenerable(w.Path, e) })
	if len(all) == 0 {
		return out
	}
	var asked []string
	for _, e := range all {
		if !strings.HasSuffix(e, "/.venv/") && e != ".venv/" {
			asked = append(asked, e)
		}
	}
	confirmed := all[:0:0]
	if len(asked) > 0 {
		said, err := gitEnv(w.Path, nil, []byte(strings.Join(asked, "\x00")+"\x00"), "-c", "core.fsmonitor=false", "check-ignore", "--stdin", "-z")
		var exit *exec.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
			out.Err = err
			return out
		}
		confirmed = strings.FieldsFunc(said, func(r rune) bool { return r == 0 })
	}
	for _, e := range all {
		if !slices.Contains(asked, e) {
			confirmed = append(confirmed, e)
		}
	}
	before, statErr := os.Stat(w.Path)
	for _, e := range confirmed {
		p := filepath.Join(w.Path, e)
		if dry {
			out.Size += Size(p)
		} else if err := os.RemoveAll(p); err != nil {
			out.Err = err
			continue
		}
		out.Entries[filepath.Base(strings.TrimSuffix(e, "/"))]++
	}
	if !dry && statErr == nil {
		os.Chtimes(w.Path, before.ModTime(), before.ModTime())
	}
	return out
}
