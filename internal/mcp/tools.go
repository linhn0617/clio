package mcp

import (
	"context"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/search"
	"github.com/linhn0617/clio/internal/sessions"
	"github.com/linhn0617/clio/internal/timeutil"
)

const (
	defaultSearchLimit = 10
	maxSearchLimit     = 50
	defaultReadLimit   = 50
	maxReadLimit       = 200
	defaultAskSessions = 6
	maxAskSessions     = 10
)

// parseSince is the MCP-side wrapper: same parsing as the CLI, but a bad value
// degrades to 0 (no bound) rather than erroring out a tool call.
func parseSince(s string) int64 {
	ts, _ := timeutil.ParseSince(s)
	return ts
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

func handleSearch(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError("query is required"), nil
		}
		res, err := search.Search(ctx, database, search.Options{
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

func handleListSessions(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		rows, err := sessions.ListSessions(ctx, database, sessions.ListFilter{
			Since:         parseSince(req.GetString("since", "")),
			ProjectPrefix: req.GetString("project", ""),
			MinTurns:      req.GetInt("min_turns", 0),
			Limit:         clamp(req.GetInt("limit", 20), 20, maxSearchLimit),
			Touched:       req.GetString("touched", ""),
			Tool:          req.GetString("tool", ""),
			Ran:           req.GetString("ran", ""),
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

func handleActivitySummary(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		since := parseSince(req.GetString("since", "7d"))
		if since == 0 {
			since = time.Now().Add(-7 * 24 * time.Hour).Unix()
		}
		groupBy := req.GetString("group_by", "day")
		switch groupBy {
		case "day", "project":
			buckets, err := sessions.ActivitySummary(ctx, database, since, groupBy)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{
				"since":   tsString(since),
				"buckets": buckets,
			})
		case "file", "command", "tool", "pattern", "url":
			counts, err := sessions.ActivityByKind(ctx, database, groupBy, since,
				req.GetString("project", ""), clamp(req.GetInt("limit", 30), 30, maxSearchLimit))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			type ac struct {
				Value string `json:"value"`
				Count int    `json:"count"`
			}
			out := make([]ac, 0, len(counts))
			for _, c := range counts {
				out = append(out, ac{c.Value, c.Count})
			}
			return mcp.NewToolResultJSON(map[string]any{
				"since":    tsString(since),
				"group_by": groupBy,
				"activity": out,
			})
		default:
			return mcp.NewToolResultError(`group_by must be one of "day", "project", "file", "command", "tool", "pattern", "url"`), nil
		}
	}
}

func handleReadSession(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		uuid, err := req.RequireString("uuid")
		if err != nil {
			return mcp.NewToolResultError("uuid is required"), nil
		}
		sess, err := sessions.ResolvePrefix(ctx, database, uuid)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		offset := req.GetInt("offset", 0)
		limit := clamp(req.GetInt("limit", defaultReadLimit), defaultReadLimit, maxReadLimit)
		msgs, hasMore, err := sessions.GetMessages(ctx, database, sess.UUID, offset, limit, req.GetBool("include_tool_output", false), false)
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

func handleAsk(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		question, err := req.RequireString("question")
		if err != nil || strings.TrimSpace(question) == "" {
			return mcp.NewToolResultError("question is required"), nil
		}
		ans, err := ask.Ask(ctx, database, ask.Options{
			Question:      question,
			ProjectPrefix: req.GetString("project", ""),
			Since:         parseSince(req.GetString("since", "")),
			MaxSessions:   clamp(req.GetInt("limit", defaultAskSessions), defaultAskSessions, maxAskSessions),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type excerpt struct {
			Seq   int    `json:"seq"`
			TS    string `json:"timestamp"`
			Role  string `json:"role"`
			Text  string `json:"text"`
			IsHit bool   `json:"is_hit"`
		}
		type group struct {
			SessionUUID string    `json:"session_uuid"`
			Title       string    `json:"title"`
			Project     string    `json:"project"`
			EndedAt     string    `json:"ended_at"`
			Excerpts    []excerpt `json:"excerpts"`
		}
		groups := make([]group, 0, len(ans.Groups))
		for _, g := range ans.Groups {
			ex := make([]excerpt, 0, len(g.Excerpts))
			for _, e := range g.Excerpts {
				ex = append(ex, excerpt{e.Seq, tsString(e.TS), e.Role, e.Text, e.IsHit})
			}
			groups = append(groups, group{g.SessionUUID, g.Title, g.Project, tsString(g.EndedAt), ex})
		}
		return mcp.NewToolResultJSON(map[string]any{
			"question": ans.Question,
			"groups":   groups,
			"count":    len(groups),
		})
	}
}

func tsString(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}
