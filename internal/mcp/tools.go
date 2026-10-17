package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/linhn0617/clio/internal/ask"
	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/registry"
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
	// Mirrors internal/ask's own defaultMaxTokens/minMaxTokens/maxMaxTokens
	// (unexported there), the same way defaultAskSessions/maxAskSessions
	// above already mirror ask's session-count defaults.
	defaultMaxTokens = 2000
	minMaxTokens     = 200
	maxMaxTokens     = 8000

	// Response budget: Claude Code spills tool output over 50,000 characters
	// to a file and hands the model only the path, costing a second read. The
	// default stays under that threshold; max_chars may move it within bounds.
	defaultMaxChars = 40000
	minMaxChars     = 4000
	maxMaxChars     = 48000
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

// clampRange is like clamp but also enforces a true floor: a positive value
// below min is raised to min rather than passed through. Used for max_tokens,
// where the spec requires an out-of-range value (including a small positive
// one) to be clamped into the allowed range rather than rejected — unlike the
// existing limit params, where any positive value under def is accepted as-is.
func clampRange(v, def, min, max int) int {
	if v <= 0 {
		return def
	}
	if v < min {
		return min
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
			Source:            req.GetString("source", registry.DefaultSource()),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type hit struct {
			SessionUUID   string `json:"session_uuid"`
			ParentSession string `json:"parent_session,omitempty"`
			AgentType     string `json:"agent_type,omitempty"`
			Source        string `json:"source"`
			Project       string `json:"project"`
			Role          string `json:"role"`
			Timestamp     string `json:"timestamp"`
			Snippet       string `json:"snippet"`
		}
		out := make([]hit, 0, len(res))
		for _, r := range res {
			out = append(out, hit{r.SessionUUID, r.ParentSession, r.AgentType, r.Source, r.ProjectPath, r.Role, tsString(r.TS), r.Snippet})
		}
		maxChars := maxCharsOf(req)
		resp, _, _ := fitBudget(len(out), maxChars, func(k int) map[string]any {
			m := map[string]any{"results": out[:k], "count": k}
			if k < len(out) {
				m["truncated"] = true
				m["next"] = fmt.Sprintf("showing %d of %d results to fit max_chars=%d; narrow with since/project/role or a smaller limit, or raise max_chars (max %d)", k, len(out), maxChars, maxMaxChars)
			}
			return m
		})
		return mcp.NewToolResultJSON(resp)
	}
}

func handleListSessions(database *db.DB, beforeRead func()) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if beforeRead != nil {
			beforeRead()
		}
		rows, err := sessions.ListSessions(ctx, database, sessions.ListFilter{
			Since:            parseSince(req.GetString("since", "")),
			ProjectPrefix:    req.GetString("project", ""),
			MinTurns:         req.GetInt("min_turns", 0),
			Limit:            clamp(req.GetInt("limit", 20), 20, maxSearchLimit),
			Touched:          req.GetString("touched", ""),
			Tool:             req.GetString("tool", ""),
			Ran:              req.GetString("ran", ""),
			IncludeSubagents: req.GetBool("include_subagents", false),
			Source:           req.GetString("source", registry.DefaultSource()),
		})
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type srow struct {
			UUID          string `json:"uuid"`
			ParentSession string `json:"parent_session,omitempty"`
			AgentType     string `json:"agent_type,omitempty"`
			SubagentCount int    `json:"subagent_count,omitempty"`
			Source        string `json:"source"`
			Project       string `json:"project"`
			Title         string `json:"title"`
			Started       string `json:"started_at"`
			Ended         string `json:"ended_at"`
			TurnCount     int    `json:"turn_count"`
		}
		out := make([]srow, 0, len(rows))
		for _, s := range rows {
			out = append(out, srow{s.UUID, s.ParentSession, s.AgentType, s.SubagentCount, s.Source, s.ProjectPath, s.Title, tsString(s.StartedAt), tsString(s.EndedAt), s.TurnCount})
		}
		maxChars := maxCharsOf(req)
		resp, _, _ := fitBudget(len(out), maxChars, func(k int) map[string]any {
			m := map[string]any{"sessions": out[:k], "count": k}
			if k < len(out) {
				m["truncated"] = true
				m["next"] = fmt.Sprintf("showing %d of %d sessions to fit max_chars=%d; narrow with since/project/min_turns or a smaller limit, or raise max_chars (max %d)", k, len(out), maxChars, maxMaxChars)
			}
			return m
		})
		return mcp.NewToolResultJSON(resp)
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
		maxChars := maxCharsOf(req)
		source := req.GetString("source", registry.DefaultSource())
		switch groupBy {
		case "day", "project":
			buckets, err := sessions.ActivitySummary(ctx, database, since, groupBy, req.GetString("project", ""), source)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			resp, _, _ := fitBudget(len(buckets), maxChars, func(k int) map[string]any {
				m := map[string]any{"since": tsString(since), "buckets": buckets[:k]}
				if k < len(buckets) {
					m["truncated"] = true
					m["next"] = fmt.Sprintf("showing %d of %d buckets to fit max_chars=%d; use a shorter since or a project filter, or raise max_chars (max %d)", k, len(buckets), maxChars, maxMaxChars)
				}
				return m
			})
			return mcp.NewToolResultJSON(resp)
		case "file", "command", "tool", "pattern", "url":
			counts, err := sessions.ActivityByKind(ctx, database, groupBy, since,
				req.GetString("project", ""), source, clamp(req.GetInt("limit", 30), 30, maxSearchLimit))
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
			resp, _, _ := fitBudget(len(out), maxChars, func(k int) map[string]any {
				m := map[string]any{"since": tsString(since), "group_by": groupBy, "activity": out[:k]}
				if k < len(out) {
					m["truncated"] = true
					m["next"] = fmt.Sprintf("showing %d of %d rows to fit max_chars=%d; use a smaller limit or a project filter, or raise max_chars (max %d)", k, len(out), maxChars, maxMaxChars)
				}
				return m
			})
			return mcp.NewToolResultJSON(resp)
		case "usage":
			// Session-level token aggregates with source attribution and
			// jump-through ids for read_session. Per-source subtotals only —
			// no cross-source grand total (tokenizers are not comparable).
			// Quota snapshot data is CLI-only by spec and never appears here.
			limit := clamp(req.GetInt("limit", 20), 20, maxSearchLimit)
			totals, err := sessions.UsageSourceTotals(ctx, database, since, req.GetString("project", ""), source, "")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			rows, err := sessions.UsageBySession(ctx, database, since, req.GetString("project", ""), source, "", limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			resp, _, _ := fitBudget(len(rows), maxChars, func(k int) map[string]any {
				m := map[string]any{
					"since":    tsString(since),
					"group_by": "usage",
					"note":     "token counts are per source and not comparable across sources; stale=true means the last usage scan failed and values are last-known, not current",
					"totals":   totals,
					"sessions": rows[:k],
				}
				if k < len(rows) {
					m["truncated"] = true
					m["next"] = fmt.Sprintf("showing %d of %d sessions to fit max_chars=%d; use a smaller limit or a project filter, or raise max_chars (max %d)", k, len(rows), maxChars, maxMaxChars)
				}
				return m
			})
			return mcp.NewToolResultJSON(resp)
		default:
			return mcp.NewToolResultError(`group_by must be one of "day", "project", "file", "command", "tool", "pattern", "url", "usage"`), nil
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
		sess, err := sessions.ResolvePrefix(ctx, database, uuid, req.GetString("source", registry.DefaultSource()))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := clamp(req.GetInt("limit", defaultReadLimit), defaultReadLimit, maxReadLimit)
		inclTool := req.GetBool("include_tool_output", false)

		// Three mutually exclusive ways to pick the page. offset=0 counts as
		// unset because the tool schema advertises 0 as its default and
		// clients may echo defaults back; around_sequence=0 is a real value,
		// so that one is detected by presence.
		offset := req.GetInt("offset", 0)
		lastN := req.GetInt("last_n", 0)
		aroundArg, hasAround := req.GetArguments()["around_sequence"]
		hasAround = hasAround && aroundArg != nil
		modes := 0
		for _, set := range []bool{offset != 0, lastN != 0, hasAround} {
			if set {
				modes++
			}
		}
		if modes > 1 {
			return mcp.NewToolResultError("offset, last_n and around_sequence are mutually exclusive: pass at most one"), nil
		}
		if offset < 0 || lastN < 0 {
			return mcp.NewToolResultError("offset and last_n must not be negative"), nil
		}
		total, err := sessions.CountMessages(ctx, database, sess.UUID, inclTool, nil)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		switch {
		case lastN > 0:
			// The page is the tail, so its size is last_n (capped like limit)
			// and the start offset is derived from the total.
			limit = min(lastN, maxReadLimit)
			offset = max(0, total-limit)
		case hasAround:
			seq := req.GetInt("around_sequence", 0)
			before, err := sessions.CountMessages(ctx, database, sess.UUID, inclTool, &seq)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			offset = max(0, before-limit/2)
		}
		msgs, hasMore, err := sessions.GetMessages(ctx, database, sess.UUID, offset, limit, inclTool, false)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		children, err := sessions.ListSessions(ctx, database, sessions.ListFilter{ParentSession: sess.UUID, Limit: 1000})
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
		type sub struct {
			UUID      string `json:"uuid"`
			AgentType string `json:"agent_type,omitempty"`
			Title     string `json:"title"`
			Messages  []m    `json:"messages,omitempty"`
			HasMore   bool   `json:"has_more,omitempty"`
		}
		inclSub := req.GetBool("include_subagents", false)
		subs := make([]sub, 0, len(children))
		for _, c := range children {
			row := sub{UUID: c.UUID, AgentType: c.AgentType, Title: c.Title}
			if inclSub {
				// Bounded by the same limit as the parent; has_more tells the client a
				// long subagent was truncated (paginate it via read_session on its uuid).
				cmsgs, cMore, err := sessions.GetMessages(ctx, database, c.UUID, 0, limit, req.GetBool("include_tool_output", false), false)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				cm := make([]m, 0, len(cmsgs))
				for _, x := range cmsgs {
					cm = append(cm, m{x.Seq, tsString(x.TS), x.Role, x.Content})
				}
				row.Messages = cm
				row.HasMore = cMore
			}
			subs = append(subs, row)
		}
		maxChars := maxCharsOf(req)
		sessionInfo := map[string]any{
			"uuid": sess.UUID, "project": sess.ProjectPath, "title": sess.Title,
			"started_at": tsString(sess.StartedAt), "turn_count": sess.TurnCount,
			"parent_session": sess.ParentSession, "agent_type": sess.AgentType,
		}
		build := func(stripSubs bool) (map[string]any, bool) {
			subsOut := subs
			if stripSubs {
				// Last resort when even one parent message plus the inlined
				// subagent transcripts overflow: keep the subagent list but
				// drop their messages (each is still readable by its uuid).
				subsOut = make([]sub, len(subs))
				for i, c := range subs {
					if len(c.Messages) > 0 {
						c.Messages, c.HasMore = nil, true
					}
					subsOut[i] = c
				}
			}
			resp, _, oversized := fitBudget(len(out), maxChars, func(k int) map[string]any {
				m := map[string]any{
					"session":   sessionInfo,
					"messages":  out[:k],
					"subagents": subsOut,
					"offset":    offset,
					"total":     total,
					"has_more":  hasMore,
				}
				if k < len(out) {
					m["has_more"] = true
					m["truncated"] = true
					m["next_offset"] = offset + k
					m["next"] = fmt.Sprintf("showing %d of %d requested messages to fit max_chars=%d; continue with offset=%d, use a smaller limit, or raise max_chars (max %d)", k, len(out), maxChars, offset+k, maxMaxChars)
				}
				return m
			})
			return resp, oversized
		}
		resp, oversized := build(false)
		if oversized && inclSub {
			resp, _ = build(true)
		}
		return mcp.NewToolResultJSON(resp)
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
			MaxTokens:     clampRange(req.GetInt("max_tokens", defaultMaxTokens), defaultMaxTokens, minMaxTokens, maxMaxTokens),
			Source:        req.GetString("source", registry.DefaultSource()),
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
			Source      string    `json:"source"`
			Excerpts    []excerpt `json:"excerpts"`
		}
		groups := make([]group, 0, len(ans.Groups))
		for _, g := range ans.Groups {
			ex := make([]excerpt, 0, len(g.Excerpts))
			for _, e := range g.Excerpts {
				ex = append(ex, excerpt{e.Seq, tsString(e.TS), e.Role, e.Text, e.IsHit})
			}
			groups = append(groups, group{g.SessionUUID, g.Title, g.Project, tsString(g.EndedAt), g.Source, ex})
		}
		// The budget unit is one excerpt, so a single huge session group can
		// shrink without dropping the whole group; groups left with no
		// excerpt disappear. Groups keep their relevance order.
		totalEx := 0
		for _, g := range groups {
			totalEx += len(g.Excerpts)
		}
		maxChars := maxCharsOf(req)
		resp, _, _ := fitBudget(totalEx, maxChars, func(k int) map[string]any {
			if k >= totalEx {
				return map[string]any{"question": ans.Question, "groups": groups, "count": len(groups)}
			}
			kept := make([]group, 0, len(groups))
			left := k
			for _, g := range groups {
				if left == 0 {
					break
				}
				n := min(left, len(g.Excerpts))
				g.Excerpts = g.Excerpts[:n]
				left -= n
				kept = append(kept, g)
			}
			return map[string]any{
				"question":  ans.Question,
				"groups":    kept,
				"count":     len(kept),
				"truncated": true,
				"next":      fmt.Sprintf("included %d of %d sessions (%d of %d excerpts) to fit max_chars=%d; narrow with since/project, lower max_tokens or limit, or raise max_chars (max %d)", len(kept), len(groups), k, totalEx, maxChars, maxMaxChars),
			}
		})
		return mcp.NewToolResultJSON(resp)
	}
}

func tsString(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}
