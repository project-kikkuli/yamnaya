package tui

import "strings"

type viewPage struct {
	lines []viewLine
	place string
}

func (p *viewPage) markScene() {
	if len(p.lines) == 0 {
		p.lines = append(p.lines, viewLine{})
	}
	p.lines[len(p.lines)-1].scene = true
}

func (p *viewPage) WriteString(text string) (int, error) {
	if len(p.lines) == 0 {
		p.lines = append(p.lines, viewLine{place: p.place})
	}
	parts := strings.Split(text, "\n")
	for i, part := range parts {
		if i > 0 {
			p.lines = append(p.lines, viewLine{place: p.place})
		}
		line := &p.lines[len(p.lines)-1]
		line.text += part
		if p.place != "" {
			line.place = p.place
		}
	}
	return len(text), nil
}

func (p viewPage) String() string {
	var b strings.Builder
	for i, line := range p.lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(line.text)
	}
	return b.String()
}

func textPage(text, place string) viewPage {
	p := viewPage{place: place}
	_, _ = p.WriteString(text)
	return p
}
