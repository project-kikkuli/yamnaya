package tui

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/repair"
)

// repairLog is a repair the board runs, its steps as they arrive under the panel that started it.
type repairLog struct {
	kind    panelKind
	title   string
	lines   []string
	verdict string
	done    bool
	at      time.Time
	ch      <-chan string
}

type repairLineMsg struct {
	log  *repairLog
	line string
	ok   bool
}

// selfStream runs this ym with args off the board, handing over each line it prints as it prints it.
func selfStream(args ...string) (<-chan string, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(self, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	ch := make(chan string, 64)
	go func() {
		defer close(ch)
		s := bufio.NewScanner(out)
		for s.Scan() {
			ch <- s.Text()
		}
		cmd.Wait()
	}()
	return ch, nil
}

// startRepair runs ym args under the open panel, showing each step it prints as it ends.
func (m Model) startRepair(title string, args ...string) (tea.Model, tea.Cmd) {
	if l := m.pn.repair; l != nil && !l.done {
		m.message = "a repair is still running"
		return m, nil
	}
	ch, err := m.pn.stream(args...)
	if err != nil {
		m.message = "error: " + err.Error()
		return m, nil
	}
	m.pn.repair = &repairLog{kind: m.pn.kind, title: title, at: time.Now(), ch: ch}
	return m, nextRepairLine(m.pn.repair)
}

func nextRepairLine(l *repairLog) tea.Cmd {
	return func() tea.Msg {
		line, ok := <-l.ch
		return repairLineMsg{l, line, ok}
	}
}

func (m Model) repairLine(msg repairLineMsg) (tea.Model, tea.Cmd) {
	l := msg.log
	if !msg.ok {
		l.done = true
		if l.verdict == "" {
			l.verdict = "the repair ended without a verdict; run ym " + strings.TrimPrefix(l.title, "ym ") + " in a terminal"
		}
		if m.pn.repair == l {
			m.message = l.verdict
		}
		return m, m.refresh(false)
	}
	if v, ok := strings.CutPrefix(msg.line, repair.VerdictPrefix); ok {
		l.verdict = v
	} else {
		l.lines = append(l.lines, msg.line)
	}
	return m, nextRepairLine(l)
}

// repairView is the running or finished repair under the panel that started it.
func (m Model) repairView(b *viewPage) {
	l := m.pn.repair
	if l == nil || l.kind != m.pn.kind {
		return
	}
	b.WriteString("\n " + pal.bold.Render(l.title) + "\n")
	for _, line := range l.lines {
		b.WriteString(" " + repairStyle(line) + "\n")
	}
	switch {
	case !l.done:
		b.WriteString(pal.dim.Render("   checking… "+time.Since(l.at).Round(time.Second).String()) + "\n")
	case l.verdict != "":
		b.WriteString(" " + pal.bold.Render(l.verdict) + "\n")
	}
}

func repairStyle(line string) string {
	switch f := strings.Fields(line); {
	case len(f) == 0:
		return line
	case f[0] == string(repair.Failed):
		return pal.red.Render(line)
	case f[0] == string(repair.Sudo):
		return pal.yellow.Render(line)
	case f[0] == string(repair.Fixed):
		return pal.green.Render(line)
	case f[0] == string(repair.OK), f[0] == string(repair.Skipped):
		return pal.dim.Render(line)
	}
	return line
}

// repairHint is the header's line saying this host has a problem ym repair fixes; a failing host's hint is on the hosts line.
func (m Model) repairHint() string {
	if m.trouble == "" {
		return ""
	}
	return "\n " + pal.yellow.Render(m.fit(m.trouble+" · Y r to repair"))
}
