package core

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	prChange = regexp.MustCompile(`\bgh\s+pr\s+(?:edit|ready|merge|close|reopen|comment|review)((?:[ \t]+[^\s;&|"'\x60\\]+)*)`)
	workdir  = regexp.MustCompile(`\bworkdir\\?"?\s*:\s*\\?"([^"\\]+)`)
	ghRemote = regexp.MustCompile(`github\.com[:/]([^/\s]+/[^/\s]+?)(?:\.git)?\s*$`)
	patch    = regexp.MustCompile(`(?s)\*\*\* Begin Patch.*?(?:\*\*\* End Patch|$)`)
)

// PRsChanged finds the pull requests a tool call edits, readies, merges, closes, reopens, comments on or reviews with gh.
// A bare number resolves against the repo of the call's workdir, or cwd when it names none; text a call writes as a patch is not run.
func PRsChanged(call, cwd string) []PR {
	if m := workdir.FindStringSubmatch(call); m != nil {
		cwd = m[1]
	}
	var out []PR
	for _, m := range prChange.FindAllStringSubmatch(patch.ReplaceAllString(call, ""), -1) {
		repo, target := "", ""
		args := strings.Fields(m[1])
		for i := 0; i < len(args); i++ {
			switch a := args[i]; {
			case a == "-R" || a == "--repo":
				if i+1 < len(args) {
					repo = args[i+1]
					i++
				}
			case strings.HasPrefix(a, "--repo="):
				repo = strings.TrimPrefix(a, "--repo=")
			case target == "" && !strings.HasPrefix(a, "-"):
				if _, err := strconv.Atoi(a); err == nil || strings.Contains(a, "/pull/") {
					target = a
				}
			}
		}
		if prs := PRsIn(target); len(prs) > 0 {
			out = append(out, prs...)
			continue
		}
		n, err := strconv.Atoi(target)
		if err != nil {
			continue
		}
		if repo == "" {
			repo = GitHubRepo(cwd)
		}
		if repo != "" {
			out = append(out, PR{URL: "https://github.com/" + repo + "/pull/" + target, Repo: repo, Number: n})
		}
	}
	return out
}

// GitHubRepo is the owner/name of dir's origin remote on GitHub, "" when it has none.
func GitHubRepo(dir string) string {
	g, ok := readGitFiles(dir)
	if !ok {
		g = revParse(dir)
	}
	if g.Common == "" {
		return ""
	}
	if m := ghRemote.FindStringSubmatch(originOf(g.Common)); m != nil {
		return m[1]
	}
	return ""
}
