package core

import (
	"regexp"
	"strings"
)

// PRBranch identifies a pushed branch in a GitHub repository.
type PRBranch struct {
	Repo, Head string
}

// Pushed records destinations that git reports updating successfully.
func (f *Facts) Pushed(command, output string) {
	if !gitPush.MatchString(command) {
		return
	}
	repo := ""
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if remote, ok := strings.CutPrefix(line, "To "); ok {
			repo = strings.TrimPrefix(OriginKey(remote), "github.com/")
			if repo == "" || repo == OriginKey(remote) {
				repo = ""
			}
			continue
		}
		if repo == "" || strings.HasPrefix(line, "!") || strings.Contains(line, "[deleted]") || strings.Contains(line, "[new tag]") {
			continue
		}
		_, target, ok := strings.Cut(line, " -> ")
		if !ok {
			continue
		}
		fields := strings.Fields(target)
		if len(fields) == 0 {
			continue
		}
		b := PRBranch{Repo: repo, Head: strings.TrimPrefix(fields[0], "refs/heads/")}
		found := false
		for _, old := range f.Pushes {
			found = found || old == b
		}
		if !found {
			f.Pushes = append(f.Pushes, b)
		}
	}
}

var gitPush = regexp.MustCompile(`\bgit\b[^\n;&|]*\bpush\b`)
