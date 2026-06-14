// Package tui is clio's interactive dashboard: a Bubble Tea presentation layer
// over the existing search / sessions / ask data layer.
package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/linhn0617/clio/internal/db"
)

// tabNames are the tab-bar labels, indexed by tab.
var tabNames = [tabCount]string{"Search", "Browse", "Activity", "Ask"}

// tab identifies a dashboard view.
type tab int

const (
	tabSearch tab = iota
	tabBrowse
	tabActivity
	tabAsk
	tabCount
)

// Model is the root dashboard model holding the active tab and shared geometry.
type Model struct {
	active        tab
	width, height int
	db            *db.DB
}

// New builds the root model over an open index.
func New(database *db.DB) Model {
	return Model{db: database}
}

func (m Model) Init() tea.Cmd { return nil }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "tab":
			m.active = (m.active + 1) % tabCount
		case "shift+tab":
			m.active = (m.active - 1 + tabCount) % tabCount
		case "1", "2", "3", "4":
			m.active = tab(msg.String()[0] - '1')
		}
	}
	return m, nil
}

// View renders the tab bar (the active tab in brackets) above the active view.
func (m Model) View() string {
	var b strings.Builder
	b.WriteString("clio ")
	for i, n := range tabNames {
		if tab(i) == m.active {
			b.WriteString(" [" + n + "]")
		} else {
			b.WriteString("  " + n)
		}
	}
	b.WriteString("\n")
	return b.String()
}
