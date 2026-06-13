package ingest

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/linhn0617/clio/internal/model"
)

// maxTargetValueBytes caps a stored activity value (commands can be long).
const maxTargetValueBytes = 512

// activityField maps a tool name to the input field and target kind describing
// what it acted on. Tools not listed contribute only a `tool` fact.
var activityField = map[string]struct{ field, kind string }{
	"Edit":         {"file_path", model.TargetFile},
	"Write":        {"file_path", model.TargetFile},
	"Read":         {"file_path", model.TargetFile},
	"MultiEdit":    {"file_path", model.TargetFile},
	"NotebookEdit": {"notebook_path", model.TargetFile},
	"Bash":         {"command", model.TargetCommand},
	"Grep":         {"pattern", model.TargetPattern},
	"Glob":         {"pattern", model.TargetPattern},
	"WebFetch":     {"url", model.TargetURL},
}

// extractTargets turns one tool_use into structured activity facts: always a
// `tool` fact (the tool name), plus a domain fact (file/command/pattern/url) when
// the tool and its input provide one. clio's own MCP tools yield nothing. Values
// are redacted and capped. The `tool` fact comes first.
func extractTargets(toolName string, input json.RawMessage) []model.ToolTarget {
	if toolName == "" || strings.HasPrefix(toolName, clioMCPToolPrefix) {
		return nil
	}
	out := []model.ToolTarget{{Kind: model.TargetTool, Value: toolName}}
	spec, ok := activityField[toolName]
	if !ok {
		return out
	}
	var m map[string]any
	if err := json.Unmarshal(input, &m); err != nil {
		return out
	}
	v, ok := m[spec.field].(string)
	if !ok || strings.TrimSpace(v) == "" {
		return out
	}
	return append(out, model.ToolTarget{Kind: spec.kind, Value: capValue(redactString(v))})
}

// capValue caps s at maxTargetValueBytes on a valid UTF-8 boundary.
func capValue(s string) string {
	if len(s) <= maxTargetValueBytes {
		return s
	}
	return trimToValidUTF8(s[:maxTargetValueBytes])
}

// BackfillActivity fills tool_targets for tool_use messages that don't have facts
// yet, from their stored (redacted) raw_json, without re-reading source files.
//
// It is order-independent and idempotent: it inserts only for messages missing
// facts, so it works whether it runs before or after normal ingest, and re-running
// is a no-op. raw_json holds the whole event, so an event with N non-clio tool_use
// blocks produced N consecutive tool_use messages; we re-parse each event once and
// align its i-th block to its i-th message (`% len(blocks)` keeps that alignment
// even for the degenerate case of two adjacent byte-identical events). The write
// runs in an IMMEDIATE transaction so concurrent clio processes can't double-insert.
func (ing *Ingester) BackfillActivity(ctx context.Context) error {
	// Cheap pre-check without the write lock: is any tool_use message missing its
	// facts? On a fully-indexed database this is the common path and returns at once.
	var pending int
	if err := ing.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM messages m WHERE m.role = ? AND NOT EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id = m.id))`,
		model.RoleToolUse).Scan(&pending); err != nil {
		return err
	}
	if pending == 0 {
		return nil
	}

	tx, err := ing.db.Begin() // IMMEDIATE (DSN _txlock=immediate): serialize across processes
	if err != nil {
		return err
	}
	defer tx.Rollback()

	type msgRow struct {
		id, ts int64
		sess   string
		raw    string
		has    bool
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT m.id, m.session_uuid, COALESCE(m.ts,0), m.raw_json,
		        EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id = m.id)
		 FROM messages m WHERE m.role = ? ORDER BY m.session_uuid, m.seq`,
		model.RoleToolUse)
	if err != nil {
		return err
	}
	var msgs []msgRow
	for rows.Next() {
		var m msgRow
		if err := rows.Scan(&m.id, &m.sess, &m.ts, &m.raw, &m.has); err != nil {
			rows.Close()
			return err
		}
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	var curRaw string
	var blocks []model.ContentBlock
	var bi int
	for _, m := range msgs {
		if m.raw != curRaw {
			curRaw, blocks, bi = m.raw, toolUseBlocks(m.raw), 0
		}
		if len(blocks) == 0 {
			continue // unparseable raw_json; skip without losing alignment for others
		}
		b := blocks[bi%len(blocks)]
		bi++
		if m.has {
			continue // already backfilled or ingested; advance alignment, don't duplicate
		}
		for _, tg := range extractTargets(b.Name, b.Input) {
			if _, err := tx.Exec(`INSERT INTO tool_targets(message_id, session_uuid, ts, kind, value) VALUES (?,?,?,?,?)`,
				m.id, m.sess, nullZero(m.ts), tg.Kind, tg.Value); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// toolUseBlocks returns the non-clio tool_use blocks of a stored event line, in
// order — matching the tool_use messages ingest created from that event.
func toolUseBlocks(raw string) []model.ContentBlock {
	var ev model.RawEvent
	if err := json.Unmarshal([]byte(raw), &ev); err != nil || ev.Message == nil {
		return nil
	}
	var blocks []model.ContentBlock
	if err := json.Unmarshal(ev.Message.Content, &blocks); err != nil {
		return nil
	}
	var out []model.ContentBlock
	for _, b := range blocks {
		if b.Type == "tool_use" && !strings.HasPrefix(b.Name, clioMCPToolPrefix) {
			out = append(out, b)
		}
	}
	return out
}
