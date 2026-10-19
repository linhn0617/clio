package db

// ActivityWatermarkSQL reads the highest message ID examined by activity backfill.
const ActivityWatermarkSQL = `SELECT COALESCE((SELECT checked_through FROM activity_backfill_state WHERE id=1),0)`

// PendingActivitySQL is shared by read-path backfill and both prune-raw guards.
// Bind the examined watermark, then model.RoleToolUse. With messages aliased m
// and NOT INDEXED, the primary-key range skips history. Pending means an
// unexamined Claude Code tool message without targets: examined unfillable rows
// no longer need raw_json for automatic backfill. Explicit index repair instead
// binds zero to revisit all history; pruned raw_json needs --full restoration.
const PendingActivitySQL = `m.id > ? AND m.role = ?
 AND m.session_uuid IN (SELECT uuid FROM sessions WHERE source IS NULL OR source = 'claude-code')
 AND NOT EXISTS(SELECT 1 FROM tool_targets tt WHERE tt.message_id = m.id)`
