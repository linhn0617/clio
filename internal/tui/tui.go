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

// Model is the root dashboard model: the active tab, shared geometry, and the
// four sub-views it routes to.
type Model struct {
	active        tab
	width, height int
	db            *db.DB
	search        searchView
	browse        browseView
	activity      activityView
	ask           askView
}

// New builds the root model over an open index.
func New(database *db.DB) Model {
	return Model{
		db:       database,
		search:   searchView{db: database},
		browse:   browseView{db: database},
		activity: activityView{db: database},
		ask:      askView{db: database},
	}
}

// Init kicks off the list loads for the tabs that show data immediately.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.browse.load(), m.activity.load())
}

// inputFocused reports whether the active tab has a focused text input, in which
// case 'q' and the digit keys are text rather than shortcuts.
func (m Model) inputFocused() bool {
	return m.active == tabSearch || m.active == tabAsk
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		// Reserve the tab-bar row when sizing the sub-views.
		return m.routeAll(tea.WindowSizeMsg{Width: msg.Width, Height: msg.Height - 1})
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			return m, tea.Quit
		case "tab":
			m.active = (m.active + 1) % tabCount
			return m, nil
		case "shift+tab":
			m.active = (m.active - 1 + tabCount) % tabCount
			return m, nil
		case "q":
			if !m.inputFocused() {
				return m, tea.Quit
			}
		case "1", "2", "3", "4":
			if !m.inputFocused() {
				m.active = tab(msg.String()[0] - '1')
				return m, nil
			}
		}
		// Any key not consumed as a shortcut goes to the focused view.
		return m.routeActive(msg)
	default:
		// Async/data messages route to every view so background results aren't lost.
		return m.routeAll(msg)
	}
}

// routeActive forwards a message to the active sub-view only.
func (m Model) routeActive(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.active {
	case tabSearch:
		m.search, cmd = m.search.Update(msg)
	case tabBrowse:
		m.browse, cmd = m.browse.Update(msg)
	case tabActivity:
		m.activity, cmd = m.activity.Update(msg)
	case tabAsk:
		m.ask, cmd = m.ask.Update(msg)
	}
	return m, cmd
}

// routeAll forwards a message to every sub-view, batching their commands. Each
// sub-view ignores message types it does not own.
func (m Model) routeAll(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd
	var c tea.Cmd
	m.search, c = m.search.Update(msg)
	cmds = append(cmds, c)
	m.browse, c = m.browse.Update(msg)
	cmds = append(cmds, c)
	m.activity, c = m.activity.Update(msg)
	cmds = append(cmds, c)
	m.ask, c = m.ask.Update(msg)
	cmds = append(cmds, c)
	return m, tea.Batch(cmds...)
}

// View renders the tab bar above the active sub-view.
func (m Model) View() string {
	return m.tabBar() + m.activeView()
}

func (m Model) tabBar() string {
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

func (m Model) activeView() string {
	switch m.active {
	case tabSearch:
		return m.search.View()
	case tabBrowse:
		return m.browse.View()
	case tabActivity:
		return m.activity.View()
	case tabAsk:
		return m.ask.View()
	}
	return ""
}
