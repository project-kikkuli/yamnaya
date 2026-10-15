package core

import (
	"os/exec"
	"testing"
)

func TestPRsChangedNamesThePRsACallActsOn(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", dir}, {"-C", dir, "remote", "add", "origin", "git@github.com:me/web.git"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	for _, c := range []struct {
		name, call, cwd string
		want            []string
	}{
		{"ready by number in a codex exec workdir", `await tools.exec_command({cmd:"gh pr ready 3061","workdir":"` + dir + `"})`, "", []string{"https://github.com/me/web/pull/3061"}},
		{"edit by number in a claude bash cwd", `{"command":"gh pr edit 12 --title x"}`, dir, []string{"https://github.com/me/web/pull/12"}},
		{"close with an explicit repo", `gh pr close 3064 -R other/api`, "", []string{"https://github.com/other/api/pull/3064"}},
		{"comment by url", `gh pr comment https://github.com/a/b/pull/7 --body ok`, "", []string{"https://github.com/a/b/pull/7"}},
		{"label value before the number", `gh pr edit --add-label ship 44`, dir, []string{"https://github.com/me/web/pull/44"}},
		{"viewing is not working on it", `gh pr view 2991 && gh pr checks 2991`, dir, nil},
		{"current branch's pr is unnamed", `gh pr ready`, dir, nil},
		{"no repo to resolve a number", `gh pr merge 5`, t.TempDir(), nil},
		{"a test fixture written by a patch", `tools.apply_patch("*** Begin Patch\n*** Update File: x_test.go\n+\tif !merged(\"gh pr merge 123\") {\n*** End Patch\n")`, dir, nil},
		{"a command after a patch", `tools.apply_patch("*** Begin Patch\n+gh pr close 4\n*** End Patch"); tools.exec_command({cmd:"gh pr merge 77"})`, dir, []string{"https://github.com/me/web/pull/77"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, pr := range PRsChanged(c.call, c.cwd) {
				got = append(got, pr.URL)
			}
			if len(got) != len(c.want) || (len(got) > 0 && got[0] != c.want[0]) {
				t.Fatalf("PRsChanged(%q) = %v, want %v", c.call, got, c.want)
			}
		})
	}
}
