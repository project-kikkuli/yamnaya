package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// scanRepos starts the one scan of this TUI session for repositories under the core's roots.
func (m *Model) scanRepos() tea.Cmd {
	if m.repoScan != nil || m.scanDone {
		return nil
	}
	if m.core.Roots == nil {
		m.scanDone = true
		return nil
	}
	ch := make(chan string, 64)
	go core.ScanRepos(m.core.Roots(), ch)
	m.repoScan = ch
	return m.waitRepos()
}

// waitRepos hands over what the scan found since the last call, waiting for at least one repository or the scan's end.
func (m Model) waitRepos() tea.Cmd {
	ch := m.repoScan
	return func() tea.Msg {
		r, ok := <-ch
		if !ok {
			return reposMsg{done: true}
		}
		batch := []string{r}
		for {
			select {
			case r, ok := <-ch:
				if !ok {
					return reposMsg{batch, true}
				}
				batch = append(batch, r)
			default:
				return reposMsg{repos: batch}
			}
		}
	}
}

func (m *Model) openRepoPicker() {
	m.startInput(inNewRepo, "", "", "", false)
	m.repoCur, m.pickErr = 0, ""
	if m.newL.Repo != "" {
		m.newL.Repo = core.MainCheckout(m.newL.Repo)
	}
	m.picks = m.orderPicks()
}

// orderPicks puts the selected row's repository first, then recently picked ones, then the scanned ones by name.
func (m Model) orderPicks() []string {
	recent, err := m.core.Store.RecentRepos()
	if err != nil {
		recent = nil
	}
	pre := m.newL.Repo
	all := core.OrderRepos(recent, m.scanned)
	if pre == "" {
		return all
	}
	return append([]string{pre}, slices.DeleteFunc(all, func(r string) bool { return r == pre })...)
}

func (m Model) repoMatches() []string { return core.MatchRepos(m.picks, string(m.input)) }

// pickedRepo is what enter in the picker chooses: a typed path, else the highlighted match, else the text as an existing directory.
func (m Model) pickedRepo(text string) (string, error) {
	if text == "" && len(m.repoMatches()) == 0 {
		return "", fmt.Errorf("no repository to pick yet; type a name or a path")
	}
	if ms := m.repoMatches(); !core.IsRepoPath(text) && len(ms) > 0 {
		return ms[max(0, min(m.repoCur, len(ms)-1))], nil
	}
	dir, err := filepath.Abs(core.ExpandHome(text))
	if err != nil {
		return "", err
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		if core.IsRepoPath(text) {
			return "", fmt.Errorf("%s is not a directory", text)
		}
		return "", fmt.Errorf("no repository matches %q", text)
	}
	return dir, nil
}

func (m Model) repoPickView() string {
	var b strings.Builder
	b.WriteString(pal.bold.Render(" PICK A REPOSITORY") + pal.dim.Render("  type to filter, or a path starting with / or ~") + "\n")
	if m.pickErr != "" {
		b.WriteString("   " + m.pickErr + "\n")
	}
	if core.IsRepoPath(string(m.input)) {
		b.WriteString(pal.dim.Render("   enter uses the path as typed") + "\n")
		return b.String()
	}
	ms := m.repoMatches()
	room := max(3, m.height-8)
	start := max(0, min(m.repoCur-room+2, len(ms)-room))
	home := core.ExpandHome("~")
	for i := start; i < len(ms) && i < start+room; i++ {
		path := ms[i]
		if rel, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
			path = "~/" + rel
		}
		b.WriteString(m.markAt(i == m.repoCur) + pad(filepath.Base(ms[i]), 28) + " " + pal.dim.Render(path) + "\n")
	}
	switch {
	case len(ms) == 0 && m.scanDone:
		b.WriteString(pal.dim.Render("   no repository matches; enter tries it as a path") + "\n")
	case !m.scanDone:
		b.WriteString(pal.dim.Render("   scanning…") + "\n")
	}
	return b.String()
}

func (m Model) markAt(on bool) string {
	if on {
		return " > "
	}
	return "   "
}
