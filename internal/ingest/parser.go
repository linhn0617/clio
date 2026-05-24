package ingest

import (
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/linhn0617/clio/internal/model"
)

// clioMCPToolPrefix identifies clio's own MCP tools, so its traffic is excluded
// from the index (self-pollution prevention). Claude Code names MCP tools
// mcp__<server>__<tool>; clio registers as the "clio" server.
const clioMCPToolPrefix = "mcp__clio__"

const maxFTSContentBytes = 64 * 1024

// EventInfo carries per-event metadata the ingester aggregates into a session.
type EventInfo struct {
	SessionID string
	CWD       string
	TS        int64
	TitleHint string // first user text, for session title
}

// Parser turns session event lines into indexable messages. It is stateful:
// it tracks clio's own tool_use ids so the matching tool_results are skipped,
// and assigns a monotonic seq.
type Parser struct {
	seq            int
	clioToolUseIDs map[string]bool
}

// NewParser returns a parser whose first emitted message has seq startSeq.
func NewParser(startSeq int) *Parser {
	return &Parser{seq: startSeq, clioToolUseIDs: map[string]bool{}}
}

// Seed preloads clio tool_use ids discovered in earlier ingest batches, so a
// tool_result arriving later is still excluded.
func (p *Parser) Seed(ids []string) {
	for _, id := range ids {
		p.clioToolUseIDs[id] = true
	}
}

// ClioToolUseIDs returns every clio tool_use id seen (seeded + discovered).
func (p *Parser) ClioToolUseIDs() []string {
	out := make([]string, 0, len(p.clioToolUseIDs))
	for id := range p.clioToolUseIDs {
		out = append(out, id)
	}
	return out
}

// ParseLine parses one .jsonl line into zero or more messages plus event info.
// A malformed line returns an error; callers decide whether to skip+warn.
func (p *Parser) ParseLine(line []byte) ([]model.Message, EventInfo, error) {
	var ev model.RawEvent
	if err := json.Unmarshal(line, &ev); err != nil {
		return nil, EventInfo{}, err
	}

	info := EventInfo{SessionID: ev.SessionID, CWD: ev.CWD, TS: parseTS(ev.Timestamp)}

	if ev.Message == nil || (ev.Type != model.RoleUser && ev.Type != model.RoleAssistant) {
		return nil, info, nil // non-conversational event: skip, but keep cwd/ts
	}

	// Redact the whole event line too: raw_json is surfaced by `clio show
	// --format raw/json`, so storing it unredacted would leak secrets that the
	// content redaction removed. Use structured JSON-aware redaction.
	raw := string(redactJSON(line))
	var msgs []model.Message

	add := func(role, content string, tcs []model.ToolCall) {
		content = strings.TrimSpace(content)
		if content == "" && len(tcs) == 0 {
			return
		}
		m := model.Message{
			SessionUUID: ev.SessionID,
			Seq:         p.seq,
			TS:          info.TS,
			Role:        role,
			Content:     truncateForFTS(redactString(content)),
			RawJSON:     raw,
			ToolCalls:   tcs,
		}
		p.seq++
		msgs = append(msgs, m)
	}

	// content is either a bare JSON string or an array of blocks.
	var asString string
	if err := json.Unmarshal(ev.Message.Content, &asString); err == nil {
		add(ev.Type, asString, nil)
		if info.TitleHint == "" && ev.Type == model.RoleUser {
			info.TitleHint = titleFrom(redactString(asString))
		}
		return msgs, info, nil
	}

	var blocks []model.ContentBlock
	if err := json.Unmarshal(ev.Message.Content, &blocks); err != nil {
		return nil, info, err
	}

	for _, b := range blocks {
		switch b.Type {
		case "text":
			add(ev.Type, b.Text, nil)
			if info.TitleHint == "" && ev.Type == model.RoleUser {
				info.TitleHint = titleFrom(redactString(b.Text))
			}
		case "thinking":
			add(model.RoleThinking, b.Thinking, nil)
		case "tool_use":
			if strings.HasPrefix(b.Name, clioMCPToolPrefix) {
				p.clioToolUseIDs[b.ID] = true
				continue
			}
			summary := toolUseSummary(b.Input)
			add(model.RoleToolUse, b.Name+" "+summary, []model.ToolCall{{ToolName: b.Name, ParamsSummary: summary}})
		case "tool_result":
			if p.clioToolUseIDs[b.ToolUseID] {
				continue
			}
			add(model.RoleToolResult, toolResultText(b.Content), nil)
		}
	}
	return msgs, info, nil
}

func parseTS(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// titleFrom derives a short session title from the first user text, stripping
// Claude Code command wrappers like <command-name>/init</command-name>.
func titleFrom(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<command-") {
		if i := strings.Index(s, "<command-name>"); i >= 0 {
			rest := s[i+len("<command-name>"):]
			if j := strings.Index(rest, "</command-name>"); j >= 0 {
				return strings.TrimSpace(rest[:j])
			}
		}
	}
	const max = 100
	if utf8.RuneCountInString(s) > max {
		r := []rune(s)
		return string(r[:max])
	}
	return s
}

// toolUseSummary builds a compact "key fields" string for a tool invocation.
func toolUseSummary(input json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url", "description"} {
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok && s != "" {
				return redactString(firstLine(s, 200))
			}
		}
	}
	return ""
}

// toolResultText flattens a tool_result content (string or block array) to text.
func toolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var blocks []model.ContentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		var sb strings.Builder
		for _, b := range blocks {
			if b.Text != "" {
				sb.WriteString(b.Text)
				sb.WriteByte('\n')
			}
		}
		return sb.String()
	}
	return ""
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// truncateForFTS caps content at maxFTSContentBytes, keeping head and tail at
// valid UTF-8 boundaries. Full content is preserved separately in raw_json.
func truncateForFTS(s string) string {
	if len(s) <= maxFTSContentBytes {
		return s
	}
	half := maxFTSContentBytes / 2
	head := trimToValidUTF8(s[:half])
	tail := trimLeadingToValidUTF8(s[len(s)-half:])
	return head + "\n…[truncated]…\n" + tail
}

func trimToValidUTF8(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

func trimLeadingToValidUTF8(s string) string {
	for len(s) > 0 {
		if r, size := utf8.DecodeRuneInString(s); r != utf8.RuneError || size > 1 {
			break
		}
		s = s[1:]
	}
	return s
}
