package sessions

import (
	"context"
	"sort"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

// FileTouch is one session's activity on a file: when it last touched the file
// and with which tools. Subagent touches roll up to the parent session.
type FileTouch struct {
	SessionUUID string
	Title       string
	TS          int64 // newest touch in the session; 0 when every fact is undated
	Tools       []string
}

// FileHistory lists the Claude Code sessions whose tool calls targeted exactly
// key (the ingester-normalised path), newest first. excludeSession drops the
// calling session's own touches from the rows only: when it is a parent, its
// subagents' touches go too; when it is itself a subagent, only its own rows
// (the parent's other activity still shows). newestTS is the
// newest session-joined touch of the file over every row — including the
// caller's and beyond limit — so a staleness check sees the true latest edit.
// Orphan facts whose session was deleted never count: tool_targets has no FK.
func FileHistory(ctx context.Context, database *db.DB, key, excludeSession string, since int64, limit int) ([]FileTouch, int64, error) {
	if limit <= 0 {
		limit = 10
	}
	srcClause, srcArgs := db.SourceFilter("s.source", "")

	var newestTS int64
	nq := `SELECT COALESCE(MAX(tt.ts),0) FROM tool_targets tt JOIN sessions s ON s.uuid = tt.session_uuid WHERE tt.kind = 'file' AND tt.value = ?` + srcClause
	nargs := append([]any{key}, srcArgs...)
	if err := database.QueryRowContext(ctx, nq, nargs...).Scan(&newestTS); err != nil {
		return nil, 0, err
	}
	// MAX() makes the title deterministic under GROUP BY (a bare column would be
	// whichever row SQLite visited last when a parent's title is NULL).
	q := `SELECT COALESCE(p.uuid, s.uuid), MAX(COALESCE(p.title, s.title, '')), COALESCE(MAX(tt.ts), 0), GROUP_CONCAT(DISTINCT tc.tool_name)
		FROM tool_targets tt
		JOIN sessions s ON s.uuid = tt.session_uuid
		LEFT JOIN sessions p ON p.uuid = s.parent_session
		JOIN tool_calls tc ON tc.message_id = tt.message_id
		WHERE tt.kind = 'file' AND tt.value = ?` + srcClause
	args := append([]any{key}, srcArgs...)
	if since > 0 {
		q += ` AND tt.ts >= ?`
		args = append(args, since)
	}
	if excludeSession != "" {
		q += ` AND s.uuid <> ? AND COALESCE(p.uuid, s.uuid) <> ?`
		args = append(args, excludeSession, excludeSession)
	}
	// MAX ignores NULL; an all-undated session sorts last via the COALESCE to 0.
	q += ` GROUP BY 1 ORDER BY 3 DESC, 1 DESC LIMIT ?`
	args = append(args, limit)

	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []FileTouch
	for rows.Next() {
		var ft FileTouch
		var tools string
		if err := rows.Scan(&ft.SessionUUID, &ft.Title, &ft.TS, &tools); err != nil {
			return nil, 0, err
		}
		if tools != "" {
			ft.Tools = strings.Split(tools, ",")
			sort.Strings(ft.Tools) // GROUP_CONCAT order is unspecified
		}
		out = append(out, ft)
	}
	return out, newestTS, rows.Err()
}
