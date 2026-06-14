package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func step(t *testing.T, m Model, msg tea.Msg) Model {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(Model)
}

func key(s tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: s} }
func runes(s string) tea.KeyMsg    { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestRootTabNavigation(t *testing.T) {
	m := New(nil)
	if m.active != tabSearch {
		t.Fatalf("default tab should be Search, got %d", m.active)
	}
	m = step(t, m, key(tea.KeyTab))
	if m.active != tabBrowse {
		t.Fatalf("Tab should advance to Browse, got %d", m.active)
	}
	m = step(t, m, key(tea.KeyShiftTab))
	if m.active != tabSearch {
		t.Fatalf("Shift-Tab should return to Search, got %d", m.active)
	}
	// Tab wraps around at the end.
	for range int(tabCount) {
		m = step(t, m, key(tea.KeyTab))
	}
	if m.active != tabSearch {
		t.Fatalf("Tab should wrap back to Search, got %d", m.active)
	}
	// Number keys jump directly.
	m = step(t, m, runes("3"))
	if m.active != tabActivity {
		t.Fatalf("'3' should select Activity, got %d", m.active)
	}
}

func TestRootViewMarksActiveTab(t *testing.T) {
	m := New(nil)
	if !strings.Contains(m.View(), "[Search]") {
		t.Fatalf("view should mark Search active: %q", m.View())
	}
	m = step(t, m, key(tea.KeyTab))
	if v := m.View(); !strings.Contains(v, "[Browse]") || strings.Contains(v, "[Search]") {
		t.Fatalf("after Tab, Browse should be the active tab (not Search): %q", v)
	}
}

func TestRootQuit(t *testing.T) {
	for _, msg := range []tea.Msg{runes("q"), key(tea.KeyCtrlC)} {
		_, cmd := New(nil).Update(msg)
		if cmd == nil {
			t.Fatalf("%v should return a command", msg)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("%v should produce tea.QuitMsg, got %T", msg, cmd())
		}
	}
}
