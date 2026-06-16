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

const (
	activityListLimit = 50
	drillSessionLimit = 50
)

// activityKinds are the activity dimensions the view cycles through. Only kinds
// with a session drill-down (Touched / Ran / Tool filters) are included.
var activityKinds = []struct{ kind, label string }{
	{"file", "files"},
	{"command", "commands"},
	{"tool", "tools"},
}

// activityView shows the most frequent files / commands / tools, and drills the
// selected entry into the sessions that touched / ran / used it.
type activityView struct {
	db            *db.DB
	ctx           context.Context
	width, height int
	kindIdx       int
	entries       []sessions.ActivityCount
	selected      int
	loaded        bool
	err           error
	drillGen      int // bumps on each drill load; debounce ticks and stale results are dropped
	drill         []sessions.Session
	drillErr      error
}

// activityLoadedMsg carries the top values for a kind; keyed by kind so a load
// that finishes after the kind changed is dropped.
type activityLoadedMsg struct {
	kind    string
	entries []sessions.ActivityCount
	err     error
}

// activityDrillMsg carries the sessions for a selected entry; keyed by kind and
// value so a drill that finishes after the kind or selection changed is dropped.
type activityDrillMsg struct {
	kind     string
	value    string
	sessions []sessions.Session
	err      error
}

func (v activityView) currentKind() string  { return activityKinds[v.kindIdx].kind }
func (v activityView) currentLabel() string { return activityKinds[v.kindIdx].label }

// selectedValue is the value of the current entry, or "" when there is none.
func (v activityView) selectedValue() string {
	if v.selected >= 0 && v.selected < len(v.entries) {
		return v.entries[v.selected].Value
	}
	return ""
}

// switchKind drops the previous kind's rows, drill, and selection so the list
// never shows stale data under the new kind's label, then reloads.
func (v activityView) switchKind() (activityView, tea.Cmd) {
	v.entries, v.drill, v.drillErr = nil, nil, nil
	v.selected, v.loaded = 0, false
	return v, v.load()
}

// selectDrill drops the previous entry's drill before loading the new
// selection's, so the detail pane never shows sessions for a different value.
func (v activityView) selectDrill() (activityView, tea.Cmd) {
	v.drill, v.drillErr = nil, nil
	return v.scheduleDrill()
}

// scheduleDrill bumps the drill generation and starts the debounce timer; the
// matching detailTickMsg fires the query, so holding j/k coalesces into one drill.
func (v activityView) scheduleDrill() (activityView, tea.Cmd) {
	v.drillGen++
	return v, scheduleDetail(tabActivity, v.drillGen)
}

// load aggregates the top values of the current kind.
func (v activityView) load() tea.Cmd {
	database, ctx := v.db, orBackground(v.ctx)
	if database == nil {
		return nil
	}
	kind := v.currentKind()
	return func() tea.Msg {
		ac, err := sessions.ActivityByKind(ctx, database, kind, 0, "", activityListLimit)
		return activityLoadedMsg{kind: kind, entries: ac, err: err}
	}
}

// drillCmd lists the sessions behind the selected entry, filtered by the kind.
func (v activityView) drillCmd() tea.Cmd {
	database, value, ctx := v.db, v.selectedValue(), orBackground(v.ctx)
	if database == nil || value == "" {
		return nil
	}
	kind := v.currentKind()
	filter := sessions.ListFilter{Limit: drillSessionLimit, TargetKind: kind, TargetValue: value}
	return func() tea.Msg {
		ss, err := sessions.ListSessions(ctx, database, filter)
		return activityDrillMsg{kind: kind, value: value, sessions: ss, err: err}
	}
}

func (v activityView) Update(msg tea.Msg) (activityView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case activityLoadedMsg:
		if msg.kind == v.currentKind() {
			v.entries, v.err, v.selected, v.loaded = msg.entries, msg.err, 0, true
			v.drill, v.drillErr = nil, nil
			return v.scheduleDrill()
		}
	case activityDrillMsg:
		if msg.kind == v.currentKind() && msg.value == v.selectedValue() {
			v.drill, v.drillErr = msg.sessions, msg.err
		}
	case detailTickMsg:
		if msg.owner == tabActivity && msg.gen == v.drillGen { // debounce settled, still current
			return v, v.drillCmd()
		}
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if v.selected > 0 {
				v.selected--
				return v.selectDrill()
			}
		case "down", "j":
			if v.selected < len(v.entries)-1 {
				v.selected++
				return v.selectDrill()
			}
		case "left", "h":
			v.kindIdx = (v.kindIdx - 1 + len(activityKinds)) % len(activityKinds)
			return v.switchKind()
		case "right", "l":
			v.kindIdx = (v.kindIdx + 1) % len(activityKinds)
			return v.switchKind()
		}
	}
	return v, nil
}

// View renders the master-detail layout: the activity entries on the left, the
// drilled sessions on the right, and a status line beneath.
func (v activityView) View() string {
	return masterDetail(v.width, v.height, v.renderList, v.renderDrill(), v.statusLine())
}

func (v activityView) renderList(w, h int) string {
	if len(v.entries) == 0 {
		if v.loaded {
			return "No activity."
		}
		return "Loading…"
	}
	var lines []string
	start, end := visibleWindow(v.selected, len(v.entries), h)
	for i := start; i < end; i++ {
		e := v.entries[i]
		marker := "  "
		if i == v.selected {
			marker = previewMatchMarker
		}
		row := fmt.Sprintf("%s%s (%d)", marker, oneLine(e.Value), e.Count)
		lines = append(lines, runewidth.Truncate(row, w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (v activityView) renderDrill() string {
	if v.drillErr != nil {
		return "drill error: " + v.drillErr.Error()
	}
	if len(v.drill) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range v.drill {
		label := s.Title
		if label == "" {
			label = s.ProjectPath
		}
		b.WriteString(shortID(s.UUID) + " " + oneLine(label) + "\n")
	}
	return b.String()
}

func (v activityView) statusLine() string {
	if v.err != nil {
		return "⚠ " + v.err.Error()
	}
	return fmt.Sprintf("%s: %d · ←/→ kind · ↑/↓ navigate · tab switch view · esc quit",
		v.currentLabel(), len(v.entries))
}
