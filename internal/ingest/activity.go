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
	return extractTargetsMemo(toolName, input, nil)
}

// extractTargetsMemo is extractTargets with a shared redaction memo (see redactMemo).
func extractTargetsMemo(toolName string, input json.RawMessage, rm *redactMemo) []model.ToolTarget {
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
	value := capValue(rm.redact(v))
	if strings.TrimSpace(value) == "" {
		return out // the value was nothing but a private block: no empty fact
	}
	return append(out, model.ToolTarget{Kind: spec.kind, Value: value})
}

// NormalizeTargetPath turns an absolute path into the exact form extractTargets
// stored for it (secret-redacted, then capped), so a lookup key built from a
// path Claude passed later still matches rows written at ingest time.
func NormalizeTargetPath(abs string) string {
	return capValue(redactString(abs))
}

// capValue caps s at maxTargetValueBytes on a valid UTF-8 boundary.
func capValue(s string) string {
	if len(s) <= maxTargetValueBytes {
		return s
	}
	return trimToValidUTF8(s[:maxTargetValueBytes])
}

// activityPendingSQL deliberately uses the primary-key range rather than the role
// or session indexes: a caught-up index must not scan its historical tool_use rows.
const activityPendingSQL = `SELECT COALESCE(MAX(id),0),
 EXISTS(SELECT 1 FROM messages m NOT INDEXED WHERE m.id > ? AND m.role = 'tool_use'
 AND m.session_uuid IN (SELECT uuid FROM sessions WHERE source IS NULL OR source = 'claude-code')
 AND NOT EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id = m.id)) FROM messages`

const activityWatermarkSQL = `SELECT COALESCE((SELECT checked_through FROM activity_backfill_state WHERE id=1),0)`
const advanceActivitySQL = `INSERT INTO activity_backfill_state(id,checked_through) VALUES (1,?)
 ON CONFLICT(id) DO UPDATE SET checked_through=excluded.checked_through
 WHERE activity_backfill_state.checked_through < excluded.checked_through`

// BackfillActivity examines new Claude Code messages once, filling missing facts
// from stored raw_json and persisting progress even for unfillable messages.
func (ing *Ingester) BackfillActivity(ctx context.Context) error {
	// 0001's AUTOINCREMENT survives all later migrations. Production inserts omit
	// IDs; full re-ingest and purge never reset sqlite_sequence. Thus no future
	// message can appear at or below an examined watermark, even after deletions.
	var w, high int64
	var pending bool
	if err := ing.db.QueryRowContext(ctx, activityWatermarkSQL).Scan(&w); err != nil {
		return err
	}
	// MAX and EXISTS share one snapshot: reading MAX after the pre-check could
	// incorrectly skip a newly committed message that still needs backfilling.
	if err := ing.db.QueryRowContext(ctx, activityPendingSQL, w).Scan(&high, &pending); err != nil {
		return err
	}
	if !pending {
		if high <= w {
			return nil
		}
		// One autocommit statement needs no explicit IMMEDIATE transaction: the
		// snapshot examined every ID through high, and this monotonic UPSERT cannot
		// regress another process's progress, regardless of writer ordering.
		_, err := ing.db.ExecContext(ctx, advanceActivitySQL, high)
		return err
	}
	tx, err := ing.db.BeginTx(ctx, nil) // IMMEDIATE: serialize targets and progress across processes.
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Another process may have completed the work while we waited for the lock.
	if err := tx.QueryRowContext(ctx, activityWatermarkSQL).Scan(&w); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(id),0) FROM messages`).Scan(&high); err != nil {
		return err
	}
	type msgRow struct {
		id, ts, seq int64
		sess, raw   string
		has         bool
	}
	rows, err := tx.QueryContext(ctx, `SELECT m.id,m.session_uuid,COALESCE(m.ts,0),m.seq,m.raw_json,
 EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id=m.id)
 FROM messages m NOT INDEXED WHERE m.id > ? AND m.id <= ? AND m.role='tool_use'
 AND m.session_uuid IN (SELECT uuid FROM sessions WHERE source IS NULL OR source='claude-code')
 ORDER BY m.session_uuid,m.seq`, w, high)
	if err != nil {
		return err
	}
	var msgs []msgRow
	for rows.Next() {
		var m msgRow
		if err := rows.Scan(&m.id, &m.sess, &m.ts, &m.seq, &m.raw, &m.has); err != nil {
			rows.Close()
			return err
		}
		msgs = append(msgs, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var curRaw, curSess string
	var blocks []model.ContentBlock
	var bi int
	for _, m := range msgs {
		if m.sess != curSess || m.raw != curRaw {
			blocks, bi = toolUseBlocks(m.raw), 0
			if m.sess != curSess {
				// W can split an event. Widen the alignment window backwards through the
				// preceding equal-raw tool rows (including byte-identical adjacent events).
				// Count them modulo the block count, but never re-examine their targets.
				prefix, err := tx.QueryContext(ctx, `SELECT raw_json FROM messages WHERE session_uuid=? AND role='tool_use' AND seq<? ORDER BY seq DESC`, m.sess, m.seq)
				if err != nil {
					return err
				}
				for prefix.Next() {
					var raw string
					if err := prefix.Scan(&raw); err != nil {
						prefix.Close()
						return err
					}
					if raw != m.raw {
						break
					}
					bi++
				}
				prefix.Close()
				if err := prefix.Err(); err != nil {
					return err
				}
			}
			curSess, curRaw = m.sess, m.raw
		}
		if len(blocks) == 0 {
			continue
		}
		b := blocks[bi%len(blocks)]
		bi++
		if m.has {
			continue
		}
		for _, tg := range extractTargets(b.Name, b.Input) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO tool_targets(message_id,session_uuid,ts,kind,value) VALUES (?,?,?,?,?)`, m.id, m.sess, nullZero(m.ts), tg.Kind, tg.Value); err != nil {
				return err
			}
		}
	}
	// Include every examined ID, even malformed events and non-tool messages.
	// Any error (including commit failure) rolls back both targets and progress.
	if high > w {
		if _, err := tx.ExecContext(ctx, advanceActivitySQL, high); err != nil {
			return err
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
