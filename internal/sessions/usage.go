package sessions

import (
	"context"
	"fmt"
	"strings"

	"github.com/linhn0617/clio/internal/db"
)

// UsageSessionRow is one session's token usage (summed across its per-model
// rows) with enough identity for jump-through (`clio show <uuid>`). Token
// counts are raw; no monetary amounts exist anywhere in this schema. Stale
// means the session file's last usage scan failed: the values are retained
// last-known, not current.
type UsageSessionRow struct {
	SessionUUID   string `json:"session_uuid"`
	Source        string `json:"source"`
	ProjectPath   string `json:"project_path"`
	Title         string `json:"title"`
	AgentType     string `json:"agent_type,omitempty"` // non-empty = subagent session
	Models        string `json:"models"`               // comma-joined distinct models
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	CacheRead     int64  `json:"cache_read_tokens"`
	CacheCreation int64  `json:"cache_creation_tokens"`
	Reasoning     int64  `json:"reasoning_tokens"`
	Tool          int64  `json:"tool_tokens"`
	TotalTokens   int64  `json:"total_tokens"`
	Stale         bool   `json:"stale,omitempty"`
}

// UsageGroupRow is one project/model aggregate within a source. Aggregates
// span many sessions, so they carry a drill-down key instead of a session id;
// StaleSessions counts constituents whose usage is stale (stale sessions stay
// INCLUDED in totals — excluding them would silently understate).
type UsageGroupRow struct {
	Source        string `json:"source"`
	Key           string `json:"key"` // project path or model name
	Sessions      int64  `json:"sessions"`
	TotalTokens   int64  `json:"total_tokens"`
	StaleSessions int64  `json:"stale_sessions,omitempty"`
}

// UsageSourceTotal is one source's subtotal over every matching session (not
// just a listing page). No cross-source grand total exists by design: token
// counts from different tokenizers are not comparable.
type UsageSourceTotal struct {
	Source        string `json:"source"`
	Sessions      int64  `json:"sessions"`
	TotalTokens   int64  `json:"total_tokens"`
	StaleSessions int64  `json:"stale_sessions,omitempty"`
}

// QuotaSnapshotRow is a stored last-observed rate-limit snapshot. CLI-only by
// spec: this type must never be serialized into an MCP response.
type QuotaSnapshotRow struct {
	Source        string
	LimitID       string
	ObservedAt    int64
	UsedPercent   float64
	WindowMinutes int64
	ResetsAt      int64
	PlanType      string
}

// usageWhere builds the shared session-scope predicates for usage queries.
// modelFilter ("" = all) restricts to sessions with usage attributed to that
// model — the drill-down target for `--by model` rows.
func usageWhere(since int64, projectPrefix, source, modelFilter string) (string, []any) {
	w := ""
	var args []any
	if since > 0 {
		w += " AND COALESCE(s.ended_at, s.started_at, 0) >= ?"
		args = append(args, since)
	}
	if projectPrefix != "" {
		w += " AND s.project_path LIKE ?"
		args = append(args, projectPrefix+"%")
	}
	if source != "" && source != "all" {
		w += " AND COALESCE(s.source,'claude-code') = ?"
		args = append(args, source)
	}
	if modelFilter != "" {
		w += " AND u.model = ?"
		args = append(args, modelFilter)
	}
	return w, args
}

// UsageBySession returns the top sessions by total tokens, at most limit per
// source, ordered source then total descending. Session totals sum the
// session's per-model rows; Models lists the distinct models.
func UsageBySession(ctx context.Context, database *db.DB, since int64, projectPrefix, source, modelFilter string, limit int) ([]UsageSessionRow, error) {
	if limit <= 0 {
		limit = 20
	}
	where, args := usageWhere(since, projectPrefix, source, modelFilter)
	q := `WITH per_session AS (
		SELECT u.session_uuid,
			COALESCE(s.source,'claude-code') AS source,
			COALESCE(s.project_path,'') AS project_path,
			COALESCE(s.title,'') AS title,
			COALESCE(s.agent_type,'') AS agent_type,
			GROUP_CONCAT(DISTINCT u.model) AS models,
			SUM(u.input_tokens) AS input_tokens,
			SUM(u.output_tokens) AS output_tokens,
			SUM(u.cache_read_tokens) AS cache_read,
			SUM(u.cache_creation_tokens) AS cache_creation,
			SUM(u.reasoning_tokens) AS reasoning,
			SUM(u.tool_tokens) AS tool,
			SUM(u.total_tokens) AS total,
			COALESCE(st.usage_stale,0) AS stale
		FROM session_usage u
		JOIN sessions s ON s.uuid = u.session_uuid
		LEFT JOIN ingest_state st ON st.source_file = s.source_file
		WHERE 1=1` + where + `
		GROUP BY u.session_uuid
	), ranked AS (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY source ORDER BY total DESC, session_uuid) AS rn
		FROM per_session
	)
	SELECT session_uuid, source, project_path, title, agent_type, models,
		input_tokens, output_tokens, cache_read, cache_creation, reasoning, tool, total, stale
	FROM ranked WHERE rn <= ? ORDER BY source, total DESC, session_uuid`
	args = append(args, limit)
	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSessionRow
	for rows.Next() {
		var r UsageSessionRow
		var stale int64
		if err := rows.Scan(&r.SessionUUID, &r.Source, &r.ProjectPath, &r.Title, &r.AgentType, &r.Models,
			&r.InputTokens, &r.OutputTokens, &r.CacheRead, &r.CacheCreation, &r.Reasoning, &r.Tool, &r.TotalTokens, &stale); err != nil {
			return nil, err
		}
		r.Stale = stale != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageGrouped aggregates usage by project or model within each source, top
// limit groups per source by total tokens. Stale propagates: a group counts
// its stale constituent sessions.
func UsageGrouped(ctx context.Context, database *db.DB, by string, since int64, projectPrefix, source string, limit int) ([]UsageGroupRow, error) {
	var keyExpr string
	switch by {
	case "project":
		keyExpr = "COALESCE(s.project_path,'')"
	case "model":
		keyExpr = "u.model"
	default:
		return nil, fmt.Errorf("usage grouping must be session, project, or model")
	}
	if limit <= 0 {
		limit = 20
	}
	where, args := usageWhere(since, projectPrefix, source, "")
	q := `WITH grouped AS (
		SELECT COALESCE(s.source,'claude-code') AS source, ` + keyExpr + ` AS key,
			COUNT(DISTINCT u.session_uuid) AS sessions,
			SUM(u.total_tokens) AS total,
			COUNT(DISTINCT CASE WHEN COALESCE(st.usage_stale,0) != 0 THEN u.session_uuid END) AS stale_sessions
		FROM session_usage u
		JOIN sessions s ON s.uuid = u.session_uuid
		LEFT JOIN ingest_state st ON st.source_file = s.source_file
		WHERE 1=1` + where + `
		GROUP BY 1, 2
	), ranked AS (
		SELECT *, ROW_NUMBER() OVER (PARTITION BY source ORDER BY total DESC, key) AS rn FROM grouped
	)
	SELECT source, key, sessions, total, stale_sessions FROM ranked WHERE rn <= ? ORDER BY source, total DESC, key`
	args = append(args, limit)
	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageGroupRow
	for rows.Next() {
		var r UsageGroupRow
		if err := rows.Scan(&r.Source, &r.Key, &r.Sessions, &r.TotalTokens, &r.StaleSessions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UsageSourceTotals returns each source's subtotal over ALL matching sessions
// (independent of any listing limit), with stale-session counts for
// propagation onto the subtotal line.
func UsageSourceTotals(ctx context.Context, database *db.DB, since int64, projectPrefix, source, modelFilter string) ([]UsageSourceTotal, error) {
	where, args := usageWhere(since, projectPrefix, source, modelFilter)
	q := `SELECT COALESCE(s.source,'claude-code') AS source,
		COUNT(DISTINCT u.session_uuid),
		SUM(u.total_tokens),
		COUNT(DISTINCT CASE WHEN COALESCE(st.usage_stale,0) != 0 THEN u.session_uuid END)
	FROM session_usage u
	JOIN sessions s ON s.uuid = u.session_uuid
	LEFT JOIN ingest_state st ON st.source_file = s.source_file
	WHERE 1=1` + where + `
	GROUP BY 1 ORDER BY 1`
	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UsageSourceTotal
	for rows.Next() {
		var r UsageSourceTotal
		if err := rows.Scan(&r.Source, &r.Sessions, &r.TotalTokens, &r.StaleSessions); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SessionTotalTokens maps session uuid -> summed total tokens for the given
// uuids (annotating list/TUI rows). Absent uuid = no usage data (placeholder,
// not zero). The bool map marks stale sessions.
func SessionTotalTokens(ctx context.Context, database *db.DB, uuids []string) (map[string]int64, map[string]bool, error) {
	if len(uuids) == 0 {
		return map[string]int64{}, map[string]bool{}, nil
	}
	q := `SELECT u.session_uuid, SUM(u.total_tokens), COALESCE(st.usage_stale,0)
		FROM session_usage u
		JOIN sessions s ON s.uuid = u.session_uuid
		LEFT JOIN ingest_state st ON st.source_file = s.source_file
		WHERE u.session_uuid IN (?` + repeatPlaceholder(len(uuids)-1) + `)
		GROUP BY u.session_uuid`
	args := make([]any, len(uuids))
	for i, u := range uuids {
		args[i] = u
	}
	rows, err := database.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	totals := map[string]int64{}
	stale := map[string]bool{}
	for rows.Next() {
		var uuid string
		var total, st int64
		if err := rows.Scan(&uuid, &total, &st); err != nil {
			return nil, nil, err
		}
		totals[uuid] = total
		if st != 0 {
			stale[uuid] = true
		}
	}
	return totals, stale, rows.Err()
}

func repeatPlaceholder(n int) string {
	return strings.Repeat(",?", n)
}

// QuotaSnapshots returns every stored quota snapshot, newest observation
// first. CLI-only by spec — never expose over MCP.
func QuotaSnapshots(ctx context.Context, database *db.DB) ([]QuotaSnapshotRow, error) {
	rows, err := database.QueryContext(ctx, `SELECT source, limit_id, observed_at,
		COALESCE(used_percent,0), COALESCE(window_minutes,0), COALESCE(resets_at,0), COALESCE(plan_type,'')
		FROM quota_snapshots ORDER BY observed_at DESC, source, limit_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QuotaSnapshotRow
	for rows.Next() {
		var r QuotaSnapshotRow
		if err := rows.Scan(&r.Source, &r.LimitID, &r.ObservedAt, &r.UsedPercent, &r.WindowMinutes, &r.ResetsAt, &r.PlanType); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
