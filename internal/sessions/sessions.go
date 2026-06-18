// Package sessions provides read queries over sessions and messages, shared by
// the CLI (list/show) and the MCP server (list_sessions/read_session).
package sessions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/linhn0617/clio/internal/db"
)

// Session is a row of the sessions table.
type Session struct {
	UUID          string
	ProjectPath   string
	Title         string
	StartedAt     int64
	EndedAt       int64
	TurnCount     int
	ParentSession string // the spawning session's uuid, for a subagent transcript
	AgentType     string // subagent type (e.g. general-purpose); empty for a normal session
	SubagentCount int    // number of subagent children (top-level rows only)
}

// Message is a row of the messages table.
type Message struct {
	Seq     int
	TS      int64
	Role    string
	Content string
	RawJSON string
}

// ListFilter narrows ListSessions.
type ListFilter struct {
	Since         int64
	ProjectPrefix string
	MinTurns      int
	Limit         int
	Touched       string // only sessions whose tool calls touched a file under this path prefix
	Tool          string // only sessions that used this tool (exact name)
	Ran           string // only sessions that ran a command containing this substring
	TargetKind    string // with TargetValue: only sessions with a tool_targets row of this exact kind
	TargetValue   string // with TargetKind: only sessions with a tool_targets row of this exact value
	// IncludeSubagents lists subagent child sessions alongside top-level ones; by
	// default only top-level sessions are returned (children are nested under their
	// parent). ParentSession instead lists exactly one parent's children.
	IncludeSubagents bool
	ParentSession    string
}

// ListSessions returns sessions matching filter, most recent first.
func ListSessions(ctx context.Context, database *db.DB, f ListFilter) ([]Session, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	filterSQL, filterArgs := listFilters(f)
	q := `SELECT uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count,
		COALESCE(parent_session,''), COALESCE(agent_type,''),
		(SELECT COUNT(*) FROM sessions sub WHERE sub.parent_session = sessions.uuid)
		FROM sessions WHERE 1=1` + filterSQL
	args := append([]any{}, filterArgs...)

	// Subagent nesting: by default list only top-level sessions. A subagent child is
	// hidden only when its parent is actually present in THIS listing (same filters),
	// so a child whose parent is filtered out or unindexed is promoted, not lost.
	// ParentSession instead lists exactly one parent's children.
	if f.ParentSession != "" {
		q += " AND parent_session = ?"
		args = append(args, f.ParentSession)
	} else if !f.IncludeSubagents {
		// Match the listing's own page: a child is hidden only when its parent is on
		// this page (same filters + recency order + limit), so a recent child of an
		// off-page parent is promoted rather than lost.
		q += " AND (parent_session IS NULL OR parent_session = '' OR parent_session NOT IN (SELECT uuid FROM sessions WHERE 1=1" + filterSQL + " ORDER BY ended_at DESC, uuid DESC LIMIT ?))"
		args = append(args, filterArgs...)
		args = append(args, f.Limit)
	}
	// uuid is a deterministic tiebreaker so the page (and the parent-presence
	// subquery above) resolve ties at the LIMIT boundary identically.
	q += " ORDER BY ended_at DESC, uuid DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount,
			&s.ParentSession, &s.AgentType, &s.SubagentCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// listFilters builds the shared WHERE predicates (everything except the subagent
// nesting clause and LIMIT), so the same filters scope both the listing and the
// "is this child's parent in the listing?" subquery used for nesting.
func listFilters(f ListFilter) (string, []any) {
	q := ""
	var args []any
	if f.Since > 0 {
		q += " AND ended_at >= ?"
		args = append(args, f.Since)
	}
	if f.ProjectPrefix != "" {
		q += ` AND project_path LIKE ? ESCAPE '\'`
		args = append(args, db.EscapeLike(f.ProjectPrefix)+"%")
	}
	if f.MinTurns > 0 {
		q += " AND turn_count >= ?"
		args = append(args, f.MinTurns)
	}
	if f.Touched != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='file' AND value LIKE ? ESCAPE '\')`
		args = append(args, db.EscapeLike(f.Touched)+"%")
	}
	if f.Tool != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='tool' AND value = ?)`
		args = append(args, f.Tool)
	}
	if f.Ran != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind='command' AND value LIKE ? ESCAPE '\')`
		args = append(args, "%"+db.EscapeLike(f.Ran)+"%")
	}
	if f.TargetKind != "" && f.TargetValue != "" {
		q += ` AND uuid IN (SELECT session_uuid FROM tool_targets WHERE kind = ? AND value = ?)`
		args = append(args, f.TargetKind, f.TargetValue)
	}
	return q, args
}

// ErrNotFound / ErrAmbiguous report prefix resolution failures.
var (
	ErrNotFound  = errors.New("no session matches that id")
	ErrAmbiguous = errors.New("ambiguous session id prefix")
)

// ResolvePrefix resolves a full uuid or unambiguous prefix to a Session.
func ResolvePrefix(ctx context.Context, database *db.DB, prefix string) (Session, error) {
	const cols = `uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count, COALESCE(parent_session,''), COALESCE(agent_type,'')`
	// Exact match wins regardless of how many prefixes also match.
	var s Session
	err := database.QueryRowContext(ctx, `SELECT `+cols+` FROM sessions WHERE uuid = ?`, prefix).
		Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount, &s.ParentSession, &s.AgentType)
	switch {
	case err == nil:
		return s, nil
	case !errors.Is(err, sql.ErrNoRows):
		return Session{}, err
	}
	// No exact match: resolve by unique prefix (escaped, cap 2 to detect ambiguity).
	rows, err := database.QueryContext(ctx, `SELECT `+cols+` FROM sessions WHERE uuid LIKE ? ESCAPE '\' LIMIT 2`, db.EscapeLike(prefix)+"%")
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()
	var matches []Session
	for rows.Next() {
		var m Session
		if err := rows.Scan(&m.UUID, &m.ProjectPath, &m.Title, &m.StartedAt, &m.EndedAt, &m.TurnCount, &m.ParentSession, &m.AgentType); err != nil {
			return Session{}, err
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return Session{}, err
	}
	switch len(matches) {
	case 0:
		return Session{}, ErrNotFound
	case 1:
		return matches[0], nil
	default:
		return Session{}, ErrAmbiguous
	}
}

// rawColumn selects raw_json only when the caller needs it; otherwise an empty
// string in its place, so high-frequency readers (e.g. the TUI preview) don't
// pull the largest column off disk for content they never render.
func rawColumn(includeRaw bool) string {
	if includeRaw {
		return "raw_json"
	}
	return "''"
}

// GetMessages returns a session's messages ordered by seq, paginated.
// When includeToolOutput is false, tool_use/tool_result/thinking are omitted.
// When includeRaw is false, raw_json is not read (Message.RawJSON stays empty).
// Returns the page and whether more rows exist past offset+limit.
func GetMessages(ctx context.Context, database *db.DB, sessionUUID string, offset, limit int, includeToolOutput, includeRaw bool) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT seq, COALESCE(ts,0), role, content, ` + rawColumn(includeRaw) + ` FROM messages WHERE session_uuid = ?`
	if !includeToolOutput {
		q += " AND role IN ('user','assistant')"
	}
	q += " ORDER BY seq LIMIT ? OFFSET ?"
	rows, err := database.QueryContext(ctx, q, sessionUUID, limit+1, offset) // +1 to detect more
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Seq, &m.TS, &m.Role, &m.Content, &m.RawJSON); err != nil {
			return nil, false, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return out, hasMore, nil
}

// GetWindow returns a conversational window around a retrieval hit: up to `before`
// dialogue messages preceding the hit at hitSeq, the hit itself, and up to `after`
// following ones, ordered by seq. The window is taken in user/assistant turn space
// (when includeToolOutput is false), so tool_use/tool_result events between a
// question and its answer neither consume the window nor appear in it.
func GetWindow(ctx context.Context, database *db.DB, sessionUUID string, hitSeq, before, after int, includeToolOutput, includeRaw bool) ([]Message, error) {
	if before < 0 {
		before = 0
	}
	if after < 0 {
		after = 0
	}
	roleClause := ""
	if !includeToolOutput {
		roleClause = " AND role IN ('user','assistant')"
	}
	cols := `seq, COALESCE(ts,0), role, content, ` + rawColumn(includeRaw)

	// The hit and preceding turns, newest-first, reversed to ascending.
	pre, err := windowRows(ctx, database,
		`SELECT `+cols+` FROM messages WHERE session_uuid = ?`+roleClause+` AND seq <= ? ORDER BY seq DESC LIMIT ?`,
		sessionUUID, hitSeq, before+1)
	if err != nil {
		return nil, err
	}
	slices.Reverse(pre)

	// Following turns, ascending.
	post, err := windowRows(ctx, database,
		`SELECT `+cols+` FROM messages WHERE session_uuid = ?`+roleClause+` AND seq > ? ORDER BY seq ASC LIMIT ?`,
		sessionUUID, hitSeq, after)
	if err != nil {
		return nil, err
	}
	return append(pre, post...), nil
}

func windowRows(ctx context.Context, database *db.DB, query string, args ...any) ([]Message, error) {
	rows, err := database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		if err := rows.Scan(&m.Seq, &m.TS, &m.Role, &m.Content, &m.RawJSON); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ActivityCount is one grouped activity value with its occurrence count.
type ActivityCount struct {
	Value string
	Count int
}

// ActivityByKind returns the most frequent activity values of a kind
// (file|command|tool|pattern|url), optionally bounded by time and project.
func ActivityByKind(ctx context.Context, database *db.DB, kind string, since int64, projectPrefix string, limit int) ([]ActivityCount, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT tt.value, COUNT(*) FROM tool_targets tt`
	if projectPrefix != "" {
		q += ` JOIN sessions s ON s.uuid = tt.session_uuid`
	}
	q += ` WHERE tt.kind = ?`
	args := []any{kind}
	if projectPrefix != "" {
		q += ` AND s.project_path LIKE ? ESCAPE '\'`
		args = append(args, db.EscapeLike(projectPrefix)+"%")
	}
	if since > 0 {
		q += ` AND tt.ts >= ?`
		args = append(args, since)
	}
	q += ` GROUP BY tt.value ORDER BY COUNT(*) DESC, tt.value LIMIT ?`
	args = append(args, limit)

	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ActivityCount
	for rows.Next() {
		var a ActivityCount
		if err := rows.Scan(&a.Value, &a.Count); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Recall bundles a project's recent sessions, recently touched files, and
// recently run commands — the material for clio's session-start recall digest.
type Recall struct {
	Sessions []Session
	Files    []ActivityCount
	Commands []ActivityCount
}

// GetRecall gathers a project's recent sessions plus its most-touched files and
// most-run commands since the given time.
func GetRecall(ctx context.Context, database *db.DB, projectPrefix string, since int64, limitSessions, limitActivity int) (Recall, error) {
	sess, err := ListSessions(ctx, database, ListFilter{ProjectPrefix: projectPrefix, Since: since, Limit: limitSessions})
	if err != nil {
		return Recall{}, err
	}
	files, err := ActivityByKind(ctx, database, "file", since, projectPrefix, limitActivity)
	if err != nil {
		return Recall{}, err
	}
	commands, err := ActivityByKind(ctx, database, "command", since, projectPrefix, limitActivity)
	if err != nil {
		return Recall{}, err
	}
	return Recall{Sessions: sess, Files: files, Commands: commands}, nil
}

// Bucket is one row of an activity summary.
type Bucket struct {
	Key      string
	Sessions int
	Messages int
}

// ActivitySummary aggregates activity since a time, grouped by "day" or "project".
func ActivitySummary(ctx context.Context, database *db.DB, since int64, groupBy string) ([]Bucket, error) {
	var keyExpr string
	switch groupBy {
	case "project":
		keyExpr = "COALESCE(s.project_path,'(unknown)')"
	case "day", "":
		keyExpr = "date(s.ended_at,'unixepoch','localtime')"
	default:
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	// COALESCE to the parent's uuid only when that parent actually exists (p.uuid),
	// so a parent + its subagents count once, while orphan subagents of an absent
	// parent each count on their own.
	q := `SELECT ` + keyExpr + ` AS k, COUNT(DISTINCT COALESCE(p.uuid, s.uuid)), COUNT(m.id)
		FROM sessions s
		LEFT JOIN messages m ON m.session_uuid = s.uuid
		LEFT JOIN sessions p ON p.uuid = s.parent_session
		WHERE s.ended_at >= ?
		GROUP BY k ORDER BY k DESC`
	rows, err := database.QueryContext(ctx, q, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bucket
	for rows.Next() {
		var b Bucket
		if err := rows.Scan(&b.Key, &b.Sessions, &b.Messages); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
