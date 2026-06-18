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
	ctx           context.Context
	width, height int
	project       string // optional project-path prefix filter
	sessions      []sessions.Session
	expanded      map[string]bool               // parent uuid -> expanded in the list
	kids          map[string][]sessions.Session // parent uuid -> its subagents (lazy)
	selected      int                           // index into the flattened rows()
	loaded        bool
	err           error
	previewGen    int // bumps on each preview load; stale preview responses are dropped
	previewMsgs   []sessions.Message
	previewErr    error
}

// browseLoadedMsg carries the recent sessions loaded for the list.
type browseLoadedMsg struct {
	sessions []sessions.Session
	err      error
}

// browseRow is one flattened display row: a top-level session, or an indented
// subagent child shown under its expanded parent.
type browseRow struct {
	sess  sessions.Session
	child bool
}

// browseChildrenLoadedMsg carries a parent's subagents, fetched lazily on expand.
type browseChildrenLoadedMsg struct {
	parent   string
	children []sessions.Session
	err      error
}

// rows flattens the top-level sessions, inserting the children of any expanded
// parent immediately beneath it. Navigation and rendering operate over these rows.
func (v browseView) rows() []browseRow {
	out := make([]browseRow, 0, len(v.sessions))
	for _, s := range v.sessions {
		out = append(out, browseRow{sess: s})
		if v.expanded[s.UUID] {
			for _, c := range v.kids[s.UUID] {
				out = append(out, browseRow{sess: c, child: true})
			}
		}
	}
	return out
}

// parentRowIndex returns the flattened-row index of a top-level parent, or -1.
func (v browseView) parentRowIndex(uuid string) int {
	for i, r := range v.rows() {
		if !r.child && r.sess.UUID == uuid {
			return i
		}
	}
	return -1
}

// load fetches the recent sessions for the list.
func (v browseView) load() tea.Cmd {
	database, project, ctx := v.db, v.project, orBackground(v.ctx)
	if database == nil {
		return nil
	}
	return func() tea.Msg {
		ss, err := sessions.ListSessions(ctx, database,
			sessions.ListFilter{ProjectPrefix: project, Limit: browseListLimit})
		return browseLoadedMsg{sessions: ss, err: err}
	}
}

// loadChildren fetches a parent's subagents so they can nest under it on expand.
func (v browseView) loadChildren(parent string) tea.Cmd {
	database, ctx := v.db, orBackground(v.ctx)
	if database == nil {
		return nil
	}
	return func() tea.Msg {
		cs, err := sessions.ListSessions(ctx, database,
			sessions.ListFilter{ParentSession: parent, Limit: browseListLimit})
		return browseChildrenLoadedMsg{parent: parent, children: cs, err: err}
	}
}

func (v browseView) Update(msg tea.Msg) (browseView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case browseLoadedMsg:
		v.sessions, v.err, v.selected, v.loaded = msg.sessions, msg.err, 0, true
		v.expanded, v.kids = nil, nil
		v.previewMsgs, v.previewErr = nil, nil
		return v.loadPreview()
	case browseChildrenLoadedMsg:
		if msg.err == nil {
			_, already := v.kids[msg.parent]
			// On the FIRST arrival, keep the selection on the same session: if the
			// parent is expanded and the selection sits below it, the children inserted
			// above shift it down. A duplicate reply (re-expanded before the first load
			// returned) inserts no new rows, so it must not shift again.
			if !already && v.expanded[msg.parent] {
				if pIdx := v.parentRowIndex(msg.parent); pIdx >= 0 && v.selected > pIdx {
					v.selected += len(msg.children)
				}
			}
			if v.kids == nil {
				v.kids = map[string][]sessions.Session{}
			}
			v.kids[msg.parent] = msg.children
		}
	case previewLoadedMsg:
		if msg.owner == tabBrowse && msg.gen == v.previewGen { // ours, and not superseded
			v.previewMsgs, v.previewErr = msg.msgs, msg.err
		}
	case detailTickMsg:
		if msg.owner == tabBrowse && msg.gen == v.previewGen { // debounce settled, still current
			return v, v.previewCmd()
		}
	case tea.KeyMsg:
		// No text input on this tab: arrows and j/k navigate the flattened list;
		// Enter expands or collapses the selected parent's subagents.
		switch msg.String() {
		case "up", "k":
			if v.selected > 0 {
				v.selected--
				return v.selectPreview()
			}
		case "down", "j":
			if v.selected < len(v.rows())-1 {
				v.selected++
				return v.selectPreview()
			}
		case "enter":
			return v.toggleExpand()
		}
	}
	return v, nil
}

// selectedSession is the UUID of the current selection, or "" when there is none.
func (v browseView) selectedSession() string {
	rows := v.rows()
	if v.selected >= 0 && v.selected < len(rows) {
		return rows[v.selected].sess.UUID
	}
	return ""
}

// toggleExpand expands or collapses the selected parent's subagents, fetching them
// on first expand. A no-op on a child row or a parent with no subagents.
func (v browseView) toggleExpand() (browseView, tea.Cmd) {
	rows := v.rows()
	if v.selected < 0 || v.selected >= len(rows) {
		return v, nil
	}
	r := rows[v.selected]
	if r.child || r.sess.SubagentCount == 0 {
		return v, nil
	}
	if v.expanded == nil {
		v.expanded = map[string]bool{}
	}
	if v.expanded[r.sess.UUID] {
		v.expanded[r.sess.UUID] = false
		return v, nil
	}
	v.expanded[r.sess.UUID] = true
	if _, ok := v.kids[r.sess.UUID]; ok {
		return v, nil // children already loaded
	}
	return v, v.loadChildren(r.sess.UUID)
}

// loadPreview bumps the preview generation and starts the debounce timer; the
// matching detailTickMsg fires the query, so holding j/k coalesces into one load.
func (v browseView) loadPreview() (browseView, tea.Cmd) {
	v.previewGen++
	return v, scheduleDetail(tabBrowse, v.previewGen)
}

// previewCmd reads the selected session's messages, tagged with the current
// preview generation so a slower response for an earlier selection is dropped.
func (v browseView) previewCmd() tea.Cmd {
	return loadSessionPreview(v.ctx, v.db, v.selectedSession(), tabBrowse, v.previewGen)
}

// selectPreview drops the previous session's preview before loading the new
// selection's, so the preview pane never shows the wrong conversation.
func (v browseView) selectPreview() (browseView, tea.Cmd) {
	v.previewMsgs, v.previewErr = nil, nil
	return v.loadPreview()
}

// View renders the master-detail layout: the session list on the left, the
// selected session's preview on the right, and a status line beneath.
func (v browseView) View() string {
	return masterDetail(v.width, v.height, v.renderList,
		renderPreview(v.previewMsgs, v.previewErr, -1), v.statusLine())
}

func (v browseView) renderList(w, h int) string {
	rows := v.rows()
	if len(rows) == 0 {
		if v.loaded {
			return "No sessions."
		}
		return "Loading…"
	}
	var lines []string
	start, end := visibleWindow(v.selected, len(rows), h)
	for i := start; i < end; i++ {
		r := rows[i]
		marker := "  "
		if i == v.selected {
			marker = previewMatchMarker
		}
		label := r.sess.Title
		if label == "" {
			label = r.sess.ProjectPath
		}
		var row string
		if r.child {
			typ := r.sess.AgentType
			if typ == "" {
				typ = "subagent"
			}
			row = marker + "  ↳ " + shortID(r.sess.UUID) + " (" + typ + ") " + oneLine(label)
		} else {
			row = marker + shortID(r.sess.UUID) + " " + oneLine(label)
			if r.sess.SubagentCount > 0 {
				caret := "▸"
				if v.expanded[r.sess.UUID] {
					caret = "▾"
				}
				row += fmt.Sprintf(" %s+%d", caret, r.sess.SubagentCount)
			}
		}
		lines = append(lines, runewidth.Truncate(row, w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (v browseView) statusLine() string {
	if v.err != nil {
		return "⚠ " + v.err.Error()
	}
	return fmt.Sprintf("%d sessions · ↑/↓ navigate · ⏎ expand · tab switch view · esc quit", len(v.sessions))
}
