-- Remove the Gemini CLI source: its adapter was dropped (Gemini CLI's personal
-- OAuth tier was discontinued upstream). Delete every row it produced so no
-- orphaned `source = 'gemini'` data remains with no adapter to maintain it.
-- messages_fts is external-content and kept in sync by the messages_ad
-- AFTER DELETE trigger, so deleting from messages is enough.
-- ingest_state is cleared by the Gemini sessions' source_file, plus orphaned
-- watermarks under `.gemini/tmp/` (both path separators) whose sessions row is
-- already gone. A watermark under that root that still belongs to a surviving
-- session of another source (the old walker handed any non-`chats` .jsonl there
-- to the claude-code fallback) is kept.
DELETE FROM tool_calls WHERE message_id IN (
    SELECT id FROM messages WHERE session_uuid IN (SELECT uuid FROM sessions WHERE source = 'gemini'));
DELETE FROM tool_targets WHERE session_uuid IN (SELECT uuid FROM sessions WHERE source = 'gemini');
DELETE FROM messages WHERE session_uuid IN (SELECT uuid FROM sessions WHERE source = 'gemini');
DELETE FROM session_usage WHERE source = 'gemini'
    OR session_uuid IN (SELECT uuid FROM sessions WHERE source = 'gemini');
DELETE FROM quota_snapshots WHERE source = 'gemini';
DELETE FROM ingest_state WHERE source_file IN (SELECT source_file FROM sessions WHERE source = 'gemini')
    OR ((source_file LIKE '%/.gemini/tmp/%' OR source_file LIKE '%\.gemini\tmp\%')
        AND source_file NOT IN (SELECT source_file FROM sessions WHERE source_file IS NOT NULL));
DELETE FROM source_conflicts WHERE seen_source = 'gemini' OR conflicting_source = 'gemini';
DELETE FROM sessions WHERE source = 'gemini';
