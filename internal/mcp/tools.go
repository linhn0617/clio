package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
	"github.com/linhn0617/clio/internal/sessions"
)

const (
	defaultSearchLimit = 10
	maxSearchLimit     = 50
	defaultReadLimit   = 50
	maxReadLimit       = 200
)

// parseSince accepts "7d"/"12h"/"30m", an absolute date, or "" (=> 0).
func parseSince(s string) int64 {
	if s == "" {
		return 0
	}
	if n := len(s); n >= 2 {
		var num int
		if _, err := fmt.Sscanf(s, "%d", &num); err == nil {
			switch s[n-1] {
			case 'd':
				return time.Now().Add(-time.Duration(num) * 24 * time.Hour).Unix()
			case 'h':
				return time.Now().Add(-time.Duration(num) * time.Hour).Unix()
			case 'm':
				return time.Now().Add(-time.Duration(num) * time.Minute).Unix()
			}
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func clamp(v, def, max int) int {
	if v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

func handleSearch(database *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}
		res, err := search.Search(database, search.Options{
			Query:             query,
			Since:             parseSince(req.GetString("since", "")),
			ProjectPrefix:     req.GetString("project", ""),
			Role:              req.GetString("role", ""),
			Limit:             clamp(req.GetInt("limit", defaultSearchLimit), defaultSearchLimit, maxSearchLimit),
			IncludeToolOutput: req.GetBool("include_tool_output", false),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type hit struct {
			SessionUUID string `json:"session_uuid"`
			Project     string `json:"project"`
			Role        string `json:"role"`
			Timestamp   string `json:"timestamp"`
			Snippet     string `json:"snippet"`
		}
		out := make([]hit, 0, len(res))
		for _, r := range res {
			out = append(out, hit{r.SessionUUID, r.ProjectPath, r.Role, tsString(r.TS), r.Snippet})
		}
		return mcp.NewToolResultJSON(map[string]any{"results": out, "count": len(out)})
	}
}

func handleListSessions(database *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		rows, err := sessions.ListSessions(database, sessions.ListFilter{
			Since:         parseSince(req.GetString("since", "")),
			ProjectPrefix: req.GetString("project", ""),
			MinTurns:      req.GetInt("min_turns", 0),
			Limit:         clamp(req.GetInt("limit", 20), 20, maxSearchLimit),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type srow struct {
			UUID      string `json:"uuid"`
			Project   string `json:"project"`
			Title     string `json:"title"`
			Started   string `json:"started_at"`
			Ended     string `json:"ended_at"`
			TurnCount int    `json:"turn_count"`
		}
		out := make([]srow, 0, len(rows))
		for _, s := range rows {
			out = append(out, srow{s.UUID, s.ProjectPath, s.Title, tsString(s.StartedAt), tsString(s.EndedAt), s.TurnCount})
		}
		return mcp.NewToolResultJSON(map[string]any{"sessions": out, "count": len(out)})
	}
}

func handleActivitySummary(database *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		since := parseSince(req.GetString("since", "7d"))
		if since == 0 {
			since = time.Now().Add(-7 * 24 * time.Hour).Unix()
		}
		buckets, err := sessions.ActivitySummary(database, since, req.GetString("group_by", "day"))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultJSON(map[string]any{
			"since":   tsString(since),
			"buckets": buckets,
		})
	}
}

func handleReadSession(database *db.DB) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, err := req.RequireString("uuid")
		if err != nil {
			return mcp.NewToolResultError("uuid is required"), nil
		}
		sess, err := sessions.ResolvePrefix(database, uuid)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		offset := req.GetInt("offset", 0)
		limit := clamp(req.GetInt("limit", defaultReadLimit), defaultReadLimit, maxReadLimit)
		msgs, hasMore, err := sessions.GetMessages(database, sess.UUID, offset, limit, req.GetBool("include_tool_output", false))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type m struct {
			Seq     int    `json:"seq"`
			TS      string `json:"timestamp"`
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		out := make([]m, 0, len(msgs))
		for _, x := range msgs {
			out = append(out, m{x.Seq, tsString(x.TS), x.Role, x.Content})
		}
		return mcp.NewToolResultJSON(map[string]any{
			"session": map[string]any{
				"uuid": sess.UUID, "project": sess.ProjectPath, "title": sess.Title,
				"started_at": tsString(sess.StartedAt), "turn_count": sess.TurnCount,
			},
			"messages": out,
			"offset":   offset,
			"has_more": hasMore,
		})
	}
}

func tsString(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}
