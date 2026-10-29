package core

import (
	"os"
	"strings"
	"testing"
)

func TestABriefPagePastesCleanWithTheAskAsWritten(t *testing.T) {
	long := strings.Repeat("keep the importer's \"fast path\" & its retries ", 40)
	for _, tc := range []struct {
		name, ask string
		brief     Brief
		// asked is the ask as the page carries it, when its headings nest under the page's.
		asked string
	}{
		{"short ask", "ship the importer", brief("merge #12"), ""},
		{"quotes, apostrophes and ampersands", `fix "copy & paste" in the owner's briefs, then ship it to <main>`,
			Brief{Assessment: `the "brief" page & its <pre> block don't wrap`, Changed: "it's fixed & pushed", Uncertainty: "none",
				Next: "1) check \"b\" in the board\n2) check the owner's paste", WaitingOn: "none"}, ""},
		{"multi-paragraph ask with headings and a long line", "Fix forward.\n\n## Steering\n\n1. Reproduce it.\n2. Fix it.\n\n" + long,
			brief("verify"), "Fix forward.\n\n### Steering\n\n1. Reproduce it.\n2. Fix it.\n\n" + long},
		{"headings in the ask and the brief, with fenced code", "Ship it.\n\n# Rules\n\n## Steering\n\n```sh\n# not a heading\n```\n\n###### Deep",
			Brief{Assessment: "## Where it stands\n\non track", Changed: "none", Uncertainty: "none", Next: "### Plan\n\n1. merge", WaitingOn: "none"},
			"Ship it.\n\n### Rules\n\n#### Steering\n\n```sh\n# not a heading\n```\n\n###### Deep"},
		{"headings already below the ask's", "Ship it.\n\n### Notes\n\nkeep it short", brief("merge #12"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, tc.ask, "/repo/august")
			if _, err := w.core.WriteBrief(m.Session, tc.brief); err != nil {
				t.Fatal(err)
			}
			file, err := os.ReadFile(g.BriefPath)
			if err != nil {
				t.Fatal(err)
			}
			page, err := w.core.BriefPage(g.ID)
			if err != nil || page != string(file) {
				t.Fatalf("BriefPage(%s) = %q, %v; want the brief file\n%s", g.ID, page, err, file)
			}
			title, _, _ := strings.Cut(page, "\n")
			if want := "# " + g.ID + ": " + shortName(tc.ask); title != want {
				t.Fatalf("title %q, want %q", title, want)
			}
			asked := tc.asked
			if asked == "" {
				asked = tc.ask
			}
			asked = strings.TrimSpace(asked)
			if !strings.Contains(page, asked+"\n") {
				t.Fatalf("page lost the ask as written:\n%s", page)
			}
			var want []string
			for _, s := range tc.brief.Sections() {
				want = append(want, "## "+s[0])
				if strings.Contains(s[1], "#") {
					continue
				}
				if !strings.Contains(page, "## "+s[0]+"\n\n"+strings.TrimSpace(s[1])+"\n") {
					t.Fatalf("page lost %s as written:\n%s", s[0], page)
				}
			}
			if shortName(tc.ask) != tc.ask {
				want = append(want, "## Ask")
			}
			var got []string
			fenced := false
			for _, line := range strings.Split(page, "\n") {
				if strings.HasPrefix(line, "```") {
					fenced = !fenced
				}
				if !fenced && (strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "## ")) {
					got = append(got, line)
				}
			}
			if got = got[1:]; strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Fatalf("page headings %q, want %q", got, want)
			}
			for _, bad := range []string{"&#", "&amp;", "&quot;", "&lt;", "\x1b", "\r"} {
				if strings.Contains(page, bad) {
					t.Fatalf("page carries %q:\n%s", bad, page)
				}
			}
			for i, line := range strings.Split(page, "\n") {
				if strings.TrimRight(line, " \t") != line {
					t.Fatalf("line %d has trailing whitespace: %q", i+1, line)
				}
			}
		})
	}
}

func TestHeadingsInsideASectionNestBelowIt(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"no headings", "fix it\n\n1. reproduce", "fix it\n\n1. reproduce"},
		{"a sibling heading nests one deeper", "fix it\n\n## Steering\n\nbe fast", "fix it\n\n### Steering\n\nbe fast"},
		{"deeper levels shift with the shallowest", "# Top\n\n## Mid\n\n### Low", "### Top\n\n#### Mid\n\n##### Low"},
		{"levels cap at six", "# Top\n\n##### Five\n\n###### Six", "### Top\n\n###### Five\n\n###### Six"},
		{"headings already below stay", "#### Notes\n\n##### More", "#### Notes\n\n##### More"},
		{"indented up to three spaces, empty and closed", "   ## Indented\n##\n## Closed ##", "   ### Indented\n###\n### Closed ##"},
		{"not headings", "#hashtag\n    ## indented code\n\\## escaped\nissue #12", "#hashtag\n    ## indented code\n\\## escaped\nissue #12"},
		{"backtick fence", "## Run\n\n```sh\n# comment\n## also\n```\n## After", "### Run\n\n```sh\n# comment\n## also\n```\n### After"},
		{"tilde fence holds a shorter or other fence", "~~~~\n# a\n~~~\n```\n# b\n~~~~\n# Out", "~~~~\n# a\n~~~\n```\n# b\n~~~~\n### Out"},
		{"unclosed fence runs to the end", "## Head\n```\n# inside", "### Head\n```\n# inside"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nestHeadings(tc.in, 2); got != tc.want {
				t.Fatalf("nestHeadings(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
