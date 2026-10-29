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
	}{
		{"short ask", "ship the importer", brief("merge #12")},
		{"quotes, apostrophes and ampersands", `fix "copy & paste" in the owner's briefs, then ship it to <main>`,
			Brief{Assessment: `the "brief" page & its <pre> block don't wrap`, Changed: "it's fixed & pushed", Uncertainty: "none",
				Next: "1) check \"b\" in the board\n2) check the owner's paste", WaitingOn: "none"}},
		{"multi-paragraph ask with headings and a long line", "Fix forward.\n\n## Steering\n\n1. Reproduce it.\n2. Fix it.\n\n" + long,
			brief("verify")},
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
			if !strings.Contains(page, strings.TrimSpace(tc.ask)+"\n") {
				t.Fatalf("page lost the ask as written:\n%s", page)
			}
			for _, s := range tc.brief.Sections() {
				if !strings.Contains(page, "## "+s[0]+"\n\n"+strings.TrimSpace(s[1])+"\n") {
					t.Fatalf("page lost %s as written:\n%s", s[0], page)
				}
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
