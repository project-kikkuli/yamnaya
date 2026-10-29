package tui

import (
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func emptyBoard(t *testing.T) Model {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := New(&core.Core{Store: st, Launches: &launches{}, Health: func() (string, error) { return "ok", nil }})
	m.pn.run = func(host, stdin string, args ...string) (string, error) { return `{"running":true}`, nil }
	m.loaded, m.width, m.height = true, 120, 40
	return m
}

func TestBoardWideKeysWorkOnEmptyBoard(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want func(Model) bool
	}{
		{"B", func(m Model) bool { return m.mode == modePanel && m.pn.kind == panelProfiles }},
		{"F", func(m Model) bool { return m.mode == modePanel && m.pn.kind == panelMCP }},
		{"H", func(m Model) bool { return m.mode == modePanel && m.pn.kind == panelHosts }},
		{"Y", func(m Model) bool { return m.mode == modePanel && m.pn.kind == panelSettings }},
		{"E", func(m Model) bool { return m.mode == modePanel }},
		{"?", func(m Model) bool { return m.mode == modeHelp }},
		{"I", func(m Model) bool { return m.mode == modeItems }},
		{"U", func(m Model) bool { return m.mode == modeScreen }},
		{"h", func(m Model) bool { return m.mode == modeScreen }},
		{"L", func(m Model) bool { return m.peaks.themeSeq > 0 }},
		{"A", func(m Model) bool { return m.peaks.hidden != emptyBoard(t).peaks.hidden }},
		{"n", func(m Model) bool { return m.mode != modeList }},
		{"N", func(m Model) bool { return m.mode != modeList }},
		{"+", func(m Model) bool { return m.mode != modeList }},
		{"/", func(m Model) bool { return m.mode != modeList }},
		{"G", func(m Model) bool { return m.mode == modeConfirm }},
	} {
		t.Run(tc.key, func(t *testing.T) {
			m := press(t, emptyBoard(t), tc.key)
			if !tc.want(m) {
				t.Fatalf("%q on an empty board did nothing: mode %v, message %q", tc.key, m.mode, m.message)
			}
		})
	}
}

func TestRowScopedKeysSayNothingSelectedOnEmptyBoard(t *testing.T) {
	for _, key := range []string{"enter", "o", "r", "g", "x", "l", "u", "e", "m", "s", "S", "K", "z", "T", "p", "P", "v"} {
		t.Run(key, func(t *testing.T) {
			m := press(t, emptyBoard(t), key)
			if m.mode != modeList || m.message != "nothing selected" {
				t.Fatalf("%q: mode %v, message %q", key, m.mode, m.message)
			}
		})
	}
}
