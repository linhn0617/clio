package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/db"
)

// askView is the question tab: a question input, the ask.Ask evidence bundle
// grouped by session, and the selected group's windowed excerpts in the preview.
type askView struct {
	db            *db.DB
	width, height int
	query         string
	gen           int // bumps on each submit; stale answers are dropped
	asked         bool
	groups        []ask.EvidenceGroup
	selected      int
	err           error
}

// askAnswerMsg carries an evidence bundle, tagged with the submit generation so a
// superseded answer is dropped.
type askAnswerMsg struct {
	gen    int
	groups []ask.EvidenceGroup
	err    error
}

func (v askView) Update(msg tea.Msg) (askView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case askAnswerMsg:
		if msg.gen == v.gen { // ignore an answer the user has superseded
			v.groups, v.err, v.selected, v.asked = msg.groups, msg.err, 0, true
		}
	case tea.KeyMsg:
		// The question input is focused: arrows navigate results; Enter submits;
		// printable keys are question text.
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(v.query) != "" {
				v.gen++
				return v, v.runAsk(v.gen)
			}
		case "up":
			if v.selected > 0 {
				v.selected--
			}
		case "down":
			if v.selected < len(v.groups)-1 {
				v.selected++
			}
		case "backspace":
			if r := []rune(v.query); len(r) > 0 {
				v.query = string(r[:len(r)-1])
			}
		default:
			if msg.Type == tea.KeyRunes {
				v.query += string(msg.Runes)
			}
		}
	}
	return v, nil
}

// runAsk builds the evidence bundle for the current question, tagged with
// generation g so a stale answer can be dropped.
func (v askView) runAsk(g int) tea.Cmd {
	q, database := v.query, v.db
	return func() tea.Msg {
		if database == nil || strings.TrimSpace(q) == "" {
			return askAnswerMsg{gen: g}
		}
		ans, err := ask.Ask(context.Background(), database, ask.Options{Question: q})
		return askAnswerMsg{gen: g, groups: ans.Groups, err: err}
	}
}

// View renders the question prompt above the master-detail layout: the evidence
// groups on the left, the selected group's excerpts on the right.
func (v askView) View() string {
	header := "? " + v.query
	body := masterDetail(v.width, v.height-1, v.renderList, v.renderExcerpts(), v.statusLine())
	return header + "\n" + body
}

func (v askView) renderList(w, h int) string {
	if len(v.groups) == 0 {
		if v.asked {
			return "No evidence found."
		}
		return "Ask a question…"
	}
	var lines []string
	for i, g := range v.groups {
		if i >= h {
			break
		}
		marker := "  "
		if i == v.selected {
			marker = previewMatchMarker
		}
		label := g.Title
		if label == "" {
			label = g.Project
		}
		row := marker + shortID(g.SessionUUID) + " " + oneLine(label)
		lines = append(lines, runewidth.Truncate(row, w, "…"))
	}
	return strings.Join(lines, "\n")
}

func (v askView) renderExcerpts() string {
	if v.selected < 0 || v.selected >= len(v.groups) {
		return ""
	}
	g := v.groups[v.selected]
	label := g.Title
	if label == "" {
		label = g.Project
	}
	var b strings.Builder
	b.WriteString(shortID(g.SessionUUID) + " " + oneLine(label) + "\n\n")
	for _, e := range g.Excerpts {
		marker := "  "
		if e.IsHit {
			marker = previewMatchMarker
		}
		b.WriteString(marker + e.Role + "\n")
		b.WriteString(e.Text + "\n\n")
	}
	return b.String()
}

func (v askView) statusLine() string {
	if v.err != nil {
		return "⚠ " + v.err.Error()
	}
	if !v.asked {
		return "type a question · ⏎ ask · tab switch view · esc quit"
	}
	return fmt.Sprintf("%d sessions · ↑/↓ navigate · ⏎ ask · tab switch view · esc quit", len(v.groups))
}
