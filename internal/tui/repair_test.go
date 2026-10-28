package tui

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// streamed fakes the ym commands a repair runs, printing lines for each and recording the arguments.
func streamed(m Model, lines ...string) (Model, *[][]string) {
	ran := &[][]string{}
	m.pn.stream = func(args ...string) (<-chan string, error) {
		*ran = append(*ran, args)
		ch := make(chan string, len(lines))
		for _, l := range lines {
			ch <- l
		}
		close(ch)
		return ch, nil
	}
	return m, ran
}

// pressAll sends keys and feeds back every message their commands return until none is left, as the Bubble Tea loop would.
func pressAll(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		next, cmd := m.Update(keyOf(k))
		m = next.(Model)
		for cmd != nil {
			out := cmd()
			if _, ok := out.(repairLineMsg); !ok {
				break
			}
			next, cmd = m.Update(out)
			m = next.(Model)
		}
	}
	return m
}

func TestRepairShowsEachStepAndEndsInItsVerdict(t *testing.T) {
	for _, c := range []struct {
		name, panel, row string
		args             []string
	}{
		{"a host from H", "H", box, []string{"remote", "repair", box}},
		{"this host from Y", "Y", "repair:", []string{"repair"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, _ := panelBoard(t)
			m, ran := streamed(m, "repairing", "  ok    dns       box is 10.0.0.1", "  fail  tcp :22   no answer (timed out) (5s)", "verdict: just this VM: restart the VM")
			m = press(t, m, c.panel)
			m = toPanelRow(t, m, c.row)
			m = pressAll(t, m, "r")
			if !slices.EqualFunc(*ran, [][]string{c.args}, slices.Equal) {
				t.Fatalf("ran %v, want %v", *ran, c.args)
			}
			v := ansi.Strip(m.View().Content)
			for _, want := range []string{"dns       box is 10.0.0.1", "tcp :22   no answer", "just this VM: restart the VM"} {
				if !strings.Contains(v, want) {
					t.Errorf("view lacks %q:\n%s", want, v)
				}
			}
			if strings.Contains(v, "verdict: ") || m.message != "just this VM: restart the VM" {
				t.Errorf("message %q; view:\n%s", m.message, v)
			}
		})
	}
}

func TestRepairThatCannotStartSaysWhy(t *testing.T) {
	m, _, _ := panelBoard(t)
	m.pn.stream = func(...string) (<-chan string, error) { return nil, errors.New("no ym binary") }
	m = press(t, m, "Y", "r")
	if m.message != "error: no ym binary" {
		t.Fatalf("message %q", m.message)
	}
}

func TestHeaderHintsWhereRepairHelps(t *testing.T) {
	for _, c := range []struct {
		name    string
		set     func(*Model)
		want    []string
		without string
	}{
		{"healthy", func(*Model) {}, nil, "to repair"},
		{"host stopped answering", func(m *Model) { m.board.Remotes[0].Err = errors.New("no answer in 40s") }, []string{"@boxmachine no answer", "H r to repair"}, ""},
		{"root-owned files here", func(m *Model) { m.trouble = "⚠ root-owned files" }, []string{"⚠ root-owned files · Y r to repair"}, "H r"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, _ := panelBoard(t)
			c.set(&m)
			h := ansi.Strip(m.header())
			for _, w := range c.want {
				if !strings.Contains(h, w) {
					t.Errorf("header lacks %q:\n%s", w, h)
				}
			}
			if c.without != "" && strings.Contains(h, c.without) {
				t.Errorf("header has %q:\n%s", c.without, h)
			}
		})
	}
}
