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
// The answer is only shown while the live query still matches the question that
// produced it, so an edited or superseded question never displays stale evidence.
type askView struct {
	db            *db.DB
	ctx           context.Context
	source        string // source filter: "" / "claude-code" | "codex" | "all"
	width, height int
	query         string
	gen           int    // bumps on each submit/edit; stale answers are dropped
	loading       bool   // an ask is in flight for the current question
	answered      string // the question that `groups` answer
	groups        []ask.EvidenceGroup
	selected      int
	err           error
}

// askAnswerMsg carries an evidence bundle, tagged with the submit generation so a
// superseded answer is dropped and with the question it answers.
type askAnswerMsg struct {
	gen      int
	question string
	groups   []ask.EvidenceGroup
	err      error
}

// showsAnswer reports whether the loaded answer still matches the visible query.
func (v askView) showsAnswer() bool {
	return v.answered != "" && v.answered == v.query
}

func (v askView) Update(msg tea.Msg) (askView, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
	case askAnswerMsg:
		if msg.gen == v.gen { // ignore an answer the user has superseded
			v.groups, v.err, v.selected = msg.groups, msg.err, 0
			v.answered, v.loading = msg.question, false
		}
	case tea.KeyMsg:
		// The question input is focused: arrows navigate results; Enter submits;
		// printable keys are question text.
		switch msg.String() {
		case "enter":
			if strings.TrimSpace(v.query) != "" {
				v.gen++
				v.loading, v.groups, v.selected = true, nil, 0
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
				v.gen++ // editing supersedes any in-flight answer
				v.loading = false
			}
		default:
			if msg.Type == tea.KeyRunes {
				v.query += string(msg.Runes)
				v.gen++ // editing supersedes any in-flight answer
				v.loading = false
			}
		}
	}
	return v, nil
}

// runAsk builds the evidence bundle for the current question, tagged with
// generation g and the question it answers so a stale answer can be dropped.
func (v askView) runAsk(g int) tea.Cmd {
	q, database, source, ctx := v.query, v.db, v.source, orBackground(v.ctx)
	return func() tea.Msg {
		if database == nil || strings.TrimSpace(q) == "" {
			return askAnswerMsg{gen: g, question: q}
		}
		ans, err := ask.Ask(ctx, database, ask.Options{Question: q, Source: source})
		return askAnswerMsg{gen: g, question: q, groups: ans.Groups, err: err}
	}
}

// View renders the question prompt above the master-detail layout: the evidence
// groups on the left, the selected group's excerpts on the right.
func (v askView) View() string {
	header := clampRow("? "+v.query, v.width)
	body := masterDetail(v.width, max(v.height-1, 1), v.renderList, v.renderExcerpts(), v.statusLine())
	return header + "\n" + body
}

func (v askView) renderList(w, h int) string {
	switch {
	case v.loading:
		return "Asking…"
	case !v.showsAnswer():
		return "Ask a question…"
	case len(v.groups) == 0:
		return "No evidence found."
	}
	var lines []string
	start, end := visibleWindow(v.selected, len(v.groups), h)
	for i := start; i < end; i++ {
		g := v.groups[i]
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
	if !v.showsAnswer() || v.selected < 0 || v.selected >= len(v.groups) {
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
	switch {
	case v.err != nil:
		return "⚠ " + v.err.Error()
	case v.loading:
		return "asking… · esc quit"
	case v.showsAnswer():
		return fmt.Sprintf("%d sessions · ↑/↓ navigate · ⏎ ask · tab switch view · esc quit", len(v.groups))
	default:
		return "type a question · ⏎ ask · tab switch view · esc quit"
	}
}
