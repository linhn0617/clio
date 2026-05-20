// Package sessions provides read queries over sessions and messages, shared by
// the CLI (list/show) and the MCP server (list_sessions/read_session).
package sessions

import (
	"errors"
	"fmt"

	"github.com/linhn0617/clio/internal/db"
)

// Session is a row of the sessions table.
type Session struct {
	UUID        string
	ProjectPath string
	Title       string
	StartedAt   int64
	EndedAt     int64
	TurnCount   int
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
}

// ListSessions returns sessions matching filter, most recent first.
func ListSessions(database *db.DB, f ListFilter) ([]Session, error) {
	if f.Limit <= 0 {
		f.Limit = 50
	}
	q := `SELECT uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count
		FROM sessions WHERE 1=1`
	var args []any
	if f.Since > 0 {
		q += " AND ended_at >= ?"
		args = append(args, f.Since)
	}
	if f.ProjectPrefix != "" {
		q += " AND project_path LIKE ?"
		args = append(args, f.ProjectPrefix+"%")
	}
	if f.MinTurns > 0 {
		q += " AND turn_count >= ?"
		args = append(args, f.MinTurns)
	}
	q += " ORDER BY ended_at DESC LIMIT ?"
	args = append(args, f.Limit)

	rows, err := database.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ErrNotFound / ErrAmbiguous report prefix resolution failures.
var (
	ErrNotFound  = errors.New("no session matches that id")
	ErrAmbiguous = errors.New("ambiguous session id prefix")
)

// ResolvePrefix resolves a full uuid or unambiguous prefix to a Session.
func ResolvePrefix(database *db.DB, prefix string) (Session, error) {
	rows, err := database.Query(`SELECT uuid, COALESCE(project_path,''), COALESCE(title,''), COALESCE(started_at,0), COALESCE(ended_at,0), turn_count
		FROM sessions WHERE uuid = ? OR uuid LIKE ? LIMIT 2`, prefix, prefix+"%")
	if err != nil {
		return Session{}, err
	}
	defer rows.Close()
	var matches []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.UUID, &s.ProjectPath, &s.Title, &s.StartedAt, &s.EndedAt, &s.TurnCount); err != nil {
			return Session{}, err
		}
		matches = append(matches, s)
		// Exact match wins immediately.
		if s.UUID == prefix {
			return s, nil
		}
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

// GetMessages returns a session's messages ordered by seq, paginated.
// When includeToolOutput is false, tool_use/tool_result/thinking are omitted.
// Returns the page and whether more rows exist past offset+limit.
func GetMessages(database *db.DB, sessionUUID string, offset, limit int, includeToolOutput bool) ([]Message, bool, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT seq, COALESCE(ts,0), role, content, raw_json FROM messages WHERE session_uuid = ?`
	if !includeToolOutput {
		q += " AND role IN ('user','assistant')"
	}
	q += " ORDER BY seq LIMIT ? OFFSET ?"
	rows, err := database.Query(q, sessionUUID, limit+1, offset) // +1 to detect more
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

// Bucket is one row of an activity summary.
type Bucket struct {
	Key      string
	Sessions int
	Messages int
}

// ActivitySummary aggregates activity since a time, grouped by "day" or "project".
func ActivitySummary(database *db.DB, since int64, groupBy string) ([]Bucket, error) {
	var keyExpr string
	switch groupBy {
	case "project":
		keyExpr = "COALESCE(s.project_path,'(unknown)')"
	case "day", "":
		keyExpr = "date(s.ended_at,'unixepoch')"
	default:
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	q := `SELECT ` + keyExpr + ` AS k, COUNT(DISTINCT s.uuid), COUNT(m.id)
		FROM sessions s LEFT JOIN messages m ON m.session_uuid = s.uuid
		WHERE s.ended_at >= ?
		GROUP BY k ORDER BY k DESC`
	rows, err := database.Query(q, since)
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
