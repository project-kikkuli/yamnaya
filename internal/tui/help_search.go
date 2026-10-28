package tui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type helpBlock struct {
	lines       []string
	blankBefore bool
	indent      int
	heading     bool
	sectionEnd  bool
}

func (b helpBlock) text() string {
	return strings.Join(strings.Fields(ansi.Strip(strings.Join(b.lines, " "))), " ")
}

// helpBlocks groups help lines into logical entries: a key and its wrapped description, or a prose paragraph.
func helpBlocks(text string) []helpBlock {
	var blocks []helpBlock
	blank := false
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		plain := ansi.Strip(l)
		if strings.TrimSpace(plain) == "" {
			blank = true
			continue
		}
		indent := len(plain) - len(strings.TrimLeft(plain, " "))
		if n := len(blocks); n > 0 && !(indent <= 3 && !(indent == 1 && !blank && blocks[n-1].indent == 1)) {
			blocks[n-1].lines = append(blocks[n-1].lines, l)
		} else {
			blocks = append(blocks, helpBlock{lines: []string{l}, blankBefore: blank, indent: indent})
		}
		blank = false
	}
	for i := range blocks[:max(0, len(blocks)-1)] {
		b, next := &blocks[i], blocks[i+1]
		if next.blankBefore || next.indent != 3 {
			continue
		}
		single := len(b.lines) == 1 && !strings.Contains(strings.TrimSpace(ansi.Strip(b.lines[0])), "  ")
		b.heading = b.indent == 1 || (b.blankBefore && single)
	}
	return blocks
}

// filterHelp keeps the entries containing query, with the headings of sections that still have one; starts are the matches' first lines.
func filterHelp(text, query string) (out string, starts []int, found bool) {
	q := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if q == "" {
		return text, nil, true
	}
	blocks := helpBlocks(text)
	hit := make([]bool, len(blocks))
	for i, b := range blocks {
		hit[i] = strings.Contains(strings.ToLower(b.text()), q)
	}
	keep := slicesClone(hit)
	for i, b := range blocks {
		if !b.heading || hit[i] {
			continue
		}
		for j := i + 1; j < len(blocks) && !blocks[j].heading; j++ {
			if b.indent == 1 && blocks[j].blankBefore {
				break
			}
			if hit[j] {
				keep[i] = true
				break
			}
		}
	}
	var lines []string
	for i, b := range blocks {
		if !keep[i] {
			continue
		}
		if b.blankBefore && len(lines) > 0 {
			lines = append(lines, "")
		}
		if hit[i] {
			starts = append(starts, len(lines))
		}
		for _, l := range b.lines {
			lines = append(lines, highlightHelp(l, q))
		}
	}
	if len(starts) == 0 {
		return "", nil, false
	}
	return strings.Join(lines, "\n") + "\n", starts, true
}

func slicesClone(b []bool) []bool { return append([]bool(nil), b...) }

// highlightHelp reverses every occurrence of the lowercase query in one line, keeping the line's own colors.
func highlightHelp(line, q string) string {
	plain := ansi.Strip(line)
	low := strings.ToLower(plain)
	if len(low) != len(plain) || !strings.Contains(low, q) {
		return line
	}
	var b strings.Builder
	cells := 0
	for from := 0; ; {
		i := strings.Index(low[from:], q)
		if i < 0 {
			break
		}
		i += from
		start := ansi.StringWidth(plain[:i])
		end := start + ansi.StringWidth(plain[i:i+len(q)])
		b.WriteString(ansi.Cut(line, cells, start))
		b.WriteString("\x1b[7m" + plain[i:i+len(q)] + "\x1b[27m")
		cells, from = end, i+len(q)
	}
	b.WriteString(ansi.Cut(line, cells, ansi.StringWidth(plain)))
	return b.String()
}

// helpView is the help page as filtered by the search, or the no-match line.
func (m Model) helpView(width int) (string, []int) {
	text := helpText(width, m.legendScene())
	out, starts, found := filterHelp(text, m.helpQ)
	if !found {
		return " no help matches '" + m.helpQ + "'\n", nil
	}
	return out, starts
}

func (m Model) helpLast() int {
	text, _ := m.helpView(m.width)
	return max(0, strings.Count(ansi.Wrap(text, m.width, " "), "\n")-m.height+4)
}

// helpSearchKey edits the search while its prompt is open.
func (m Model) helpSearchKey(k tea.KeyPressMsg) Model {
	switch {
	case k.Code == tea.KeyEscape:
		m.helpQ, m.helpTyping = "", false
	case k.Code == tea.KeyEnter:
		m.helpTyping = false
	case k.Code == tea.KeyBackspace:
		if r := []rune(m.helpQ); len(r) > 0 {
			m.helpQ = string(r[:len(r)-1])
		}
	case k.String() == "ctrl+u":
		m.helpQ = ""
	case k.Text != "":
		m.helpQ += k.Text
	default:
		return m
	}
	m.scrollY, m.helpAt = 0, 0
	return m
}

// helpJump scrolls to the next (dir 1) or previous (dir -1) match.
func (m Model) helpJump(dir int) Model {
	_, starts := m.helpView(m.width)
	if len(starts) == 0 {
		return m
	}
	at := m.helpAt - 1
	if at < 0 && dir < 0 {
		at = 0
	}
	at = (at + dir + len(starts)) % len(starts)
	m.helpAt = at + 1
	m.scrollY = min(m.helpLast(), starts[at])
	return m
}
