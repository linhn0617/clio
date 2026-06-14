package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/sessions"
)

// browseListLimit bounds how many recent sessions the Browse list loads.
const browseListLimit = 50

// browseView lists recent sessions (optionally filtered by project) with a
// preview of the selected session's messages.
type browseView struct {
	db            *db.DB
	width, height int
	project       string // optional project-path prefix filter
	sessions      []sessions.Session
	selected      int
	loaded        bool
	err           error
	previewMsgs   []sessions.Message
	previewErr    error
}

// browseLoadedMsg carries the recent sessions loaded for the list.
type browseLoadedMsg struct {
	sessions []sessions.Session
	err      error
}

// load fetches the recent sessions for the list.
func (v browseView) load() tea.Cmd {
	database, project := v.db, v.project
	if database == nil {
		return nil
	}
	return func() tea.Msg {
		ss, err := sessions.ListSessions(context.Background(), database,
			sessions.ListFilter{ProjectPrefix: project, Limit: browseListLimit})
		return browseLoadedMsg{sessions: ss, err: err}
	}
}

func (v browseView) Update(msg tea.Msg) (browseView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case browseLoadedMsg:
		v.sessions, v.err, v.selected, v.loaded = msg.sessions, msg.err, 0, true
		v.previewMsgs, v.previewErr = nil, nil
		return v, v.loadPreview()
	case previewLoadedMsg:
		if msg.sessionUUID == v.selectedSession() { // ignore a load the selection moved past
			v.previewMsgs, v.previewErr = msg.msgs, msg.err
		}
	case tea.KeyMsg:
		// No text input on this tab: arrows and j/k both navigate the list.
		switch msg.String() {
		case "up", "k":
			if v.selected > 0 {
				v.selected--
				return v, v.loadPreview()
			}
		case "down", "j":
			if v.selected < len(v.sessions)-1 {
				v.selected++
				return v, v.loadPreview()
			}
		}
	}
	return v, nil
}

// selectedSession is the UUID of the current selection, or "" when there is none.
func (v browseView) selectedSession() string {
	if v.selected >= 0 && v.selected < len(v.sessions) {
		return v.sessions[v.selected].UUID
	}
	return ""
}

// loadPreview reads the selected session's messages for the preview pane.
func (v browseView) loadPreview() tea.Cmd {
	return loadSessionPreview(v.db, v.selectedSession())
}

// View renders the master-detail layout: the session list on the left, the
// selected session's preview on the right, and a status line beneath.
func (v browseView) View() string {
	return masterDetail(v.width, v.height, v.renderList,
		renderPreview(v.previewMsgs, v.previewErr, ""), v.statusLine())
}

func (v browseView) renderList(w, h int) string {
	if len(v.sessions) == 0 {
		if v.loaded {
			return "No sessions."
		}
		return "Loading…"
	}
	var lines []string
	for i, s := range v.sessions {
		if i >= h {
			break
		}
		marker := "  "
		if i == v.selected {
			marker = previewMatchMarker
		}
		label := s.Title
		if label == "" {
			label = s.ProjectPath
		}
		row := marker + shortID(s.UUID) + " " + oneLine(label)
		lines = append(lines, runewidth.Truncate(row, w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (v browseView) statusLine() string {
	if v.err != nil {
		return "⚠ " + v.err.Error()
	}
	return fmt.Sprintf("%d sessions · ↑/↓ navigate · tab switch view · esc quit", len(v.sessions))
}
