package mcp

import (
	"context"
	"fmt"
	"math"
	"slices"
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
		return fitRows(out, maxCharsOf(req),
			func(r *hit, c int) { r.Snippet = capText(r.Snippet, c) },
			func(rows []hit) map[string]any { return map[string]any{"results": rows, "count": len(rows)} },
			"results", "narrow with since/project/role or a smaller limit"), nil
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
		return fitRows(out, maxCharsOf(req),
			func(r *srow, c int) { r.Title = capText(r.Title, c) },
			func(rows []srow) map[string]any { return map[string]any{"sessions": rows, "count": len(rows)} },
			"sessions", "narrow with since/project/min_turns or a smaller limit"), nil
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
			return fitRows(buckets, maxChars, nil,
				func(rows []sessions.Bucket) map[string]any {
					return map[string]any{"since": tsString(since), "buckets": rows}
				},
				"buckets", "use a shorter since or a project filter"), nil
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
			return fitRows(out, maxChars,
				func(r *ac, c int) { r.Value = capText(r.Value, c) },
				func(rows []ac) map[string]any {
					return map[string]any{"since": tsString(since), "group_by": groupBy, "activity": rows}
				},
				"rows", "use a smaller limit or a project filter"), nil
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
			return fitRows(rows, maxChars, nil,
				func(kept []sessions.UsageSessionRow) map[string]any {
					return map[string]any{
						"since":    tsString(since),
						"group_by": "usage",
						"note":     "token counts are per source and not comparable across sources; stale=true means the last usage scan failed and values are last-known, not current",
						"totals":   totals,
						"sessions": kept,
					}
				},
				"sessions", "use a smaller limit or a project filter"), nil
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
		// clients may echo defaults back; last_n and around_sequence have no
		// default (and around_sequence=0 is a real value), so they are
		// detected by presence.
		args := req.GetArguments()
		intArg := func(name string) (int, bool) {
			switch v := args[name].(type) {
			case int:
				return v, true
			case int64:
				return int(v), true
			case float64:
				return int(v), v == math.Trunc(v)
			}
			return 0, false
		}
		offset := req.GetInt("offset", 0)
		hasLast := args["last_n"] != nil
		hasAround := args["around_sequence"] != nil
		modes := 0
		for _, set := range []bool{offset != 0, hasLast, hasAround} {
			if set {
				modes++
			}
		}
		if modes > 1 {
			return mcp.NewToolResultError("offset, last_n and around_sequence are mutually exclusive: pass at most one"), nil
		}
		if offset < 0 {
			return mcp.NewToolResultError("offset must not be negative"), nil
		}
		total, err := sessions.CountMessages(ctx, database, sess.UUID, inclTool, nil)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		type mode int
		const (
			fromOffset mode = iota
			fromEnd
			aroundSeq
		)
		pageMode, rows, target := fromOffset, limit, 0
		switch {
		case hasLast:
			n, ok := intArg("last_n")
			if !ok || n < 1 {
				return mcp.NewToolResultError("last_n must be a positive integer"), nil
			}
			// The page is the tail: as many rows as last_n asks for, but never
			// more than the page size.
			pageMode, rows = fromEnd, min(n, limit)
			offset = max(0, total-rows)
		case hasAround:
			seq, ok := intArg("around_sequence")
			maxSeq, err := sessions.MaxSeq(ctx, database, sess.UUID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			if !ok || seq < 0 || seq > maxSeq {
				return mcp.NewToolResultError(fmt.Sprintf("around_sequence must be an integer between 0 and %d (this session's last seq)", maxSeq)), nil
			}
			before, err := sessions.CountMessages(ctx, database, sess.UUID, inclTool, &seq)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			pageMode = aroundSeq
			// Centre on the target, then slide back so a window near the end
			// is still a full page (the mirror of clamping at offset 0).
			offset = max(0, min(before-limit/2, total-limit))
			target = before
		}
		msgs, hasMore, err := sessions.GetMessages(ctx, database, sess.UUID, offset, rows, inclTool, false)
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
				cmsgs, cMore, err := sessions.GetMessages(ctx, database, c.UUID, 0, limit, inclTool, false)
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
		n := len(out)
		// pick returns which k of the n fetched messages survive when the
		// budget forces a cut: the front for a forward page, the tail for
		// last_n (the end is what was asked for), a window around the target
		// for around_sequence.
		pick := func(k int) (lo, hi int) {
			switch pageMode {
			case fromEnd:
				return n - k, n
			case aroundSeq:
				t := min(max(target-offset, 0), n-1)
				lo = min(max(t-(k-1)/2, 0), n-k)
				return lo, lo + k
			}
			return 0, k
		}
		shownSubs, notes := subs, []string{}
		render := func(k, textCap int) map[string]any {
			lo, hi := pick(k)
			kept := out[lo:hi]
			if textCap >= 0 {
				kept = slices.Clone(kept)
				for i := range kept {
					kept[i].Content = capText(kept[i].Content, textCap)
				}
			}
			resp := map[string]any{
				"session":   sessionInfo,
				"messages":  kept,
				"subagents": shownSubs,
				"offset":    offset + lo,
				"total":     total,
				"has_more":  hasMore || hi < n,
			}
			if hi < n {
				resp["next_offset"] = offset + hi
			}
			if len(notes) > 0 {
				resp["truncated"] = true
				resp["next"] = strings.Join(notes, "; ")
			}
			return resp
		}

		// Shed the least valuable parts first. 1) Inlined subagent messages go
		// before any parent message does; each stays readable by its uuid.
		// 2) The subagent list itself is held to half the budget so a session
		// with hundreds of children cannot starve its own messages.
		if inclSub && measure(render(n, -1)) > maxChars {
			stripped := make([]sub, len(subs))
			for i, c := range subs {
				if len(c.Messages) > 0 {
					c.Messages, c.HasMore = nil, true
				}
				stripped[i] = c
			}
			shownSubs = stripped
			notes = append(notes, "subagent messages omitted to fit max_chars; read each by its uuid")
		}
		if len(shownSubs) > 0 && measure(render(0, -1)) > maxChars/2 {
			all := shownSubs
			best, lo, hi := 0, 0, len(all)-1
			for lo <= hi {
				mid := (lo + hi) / 2
				shownSubs = all[:mid]
				if measure(render(0, -1)) <= maxChars/2 {
					best, lo = mid, mid+1
				} else {
					hi = mid - 1
				}
			}
			shownSubs = all[:best]
			notes = append(notes, fmt.Sprintf("listed %d of %d subagents to fit max_chars", best, len(all)))
		}

		return fitBudget(n, maxChars, render, func(kept, of int) string {
			lo, hi := pick(kept)
			parts := append([]string(nil), notes...)
			parts = append(parts, fmt.Sprintf("showing messages at offsets %d-%d of the %d requested to fit max_chars=%d", offset+lo, offset+hi-1, of, maxChars))
			if hi < of {
				parts = append(parts, fmt.Sprintf("continue with offset=%d", offset+hi))
			}
			if lo > 0 {
				parts = append(parts, fmt.Sprintf("earlier ones start at offset=%d", offset))
			}
			parts = append(parts, fmt.Sprintf("or use a smaller limit, or raise max_chars (max %d)", maxMaxChars))
			return strings.Join(parts, "; ")
		}), nil
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
		// The budget unit is one excerpt. Excerpts are ranked for survival: every
		// is_hit excerpt (in group order) outranks every context excerpt, so a
		// tight budget drops context first and then whole trailing groups, never
		// the evidence a group was cited for. Groups left with no excerpt
		// disappear; kept excerpts stay in their original order.
		type ref struct{ g, e int }
		var order []ref
		for pass := 0; pass < 2; pass++ {
			for gi, g := range groups {
				for ei, e := range g.Excerpts {
					if e.IsHit == (pass == 0) {
						order = append(order, ref{gi, ei})
					}
				}
			}
		}
		render := func(k, textCap int) map[string]any {
			keep := make([][]bool, len(groups))
			for gi, g := range groups {
				keep[gi] = make([]bool, len(g.Excerpts))
			}
			for _, r := range order[:k] {
				keep[r.g][r.e] = true
			}
			kept := make([]group, 0, len(groups))
			for gi, g := range groups {
				var ex []excerpt
				for ei, e := range g.Excerpts {
					if keep[gi][ei] {
						e.Text = capText(e.Text, textCap)
						ex = append(ex, e)
					}
				}
				if len(ex) > 0 {
					g.Excerpts = ex
					kept = append(kept, g)
				}
			}
			return map[string]any{"question": ans.Question, "groups": kept, "count": len(kept)}
		}
		maxChars := maxCharsOf(req)
		return fitBudget(len(order), maxChars, render, func(kept, total int) string {
			sessionsKept := render(kept, -1)["count"]
			return fmt.Sprintf("included %d of %d sessions (%d of %d excerpts, hits first) to fit max_chars=%d; narrow with since/project, lower max_tokens or limit, or raise max_chars (max %d)", sessionsKept, len(groups), kept, total, maxChars, maxMaxChars)
		}), nil
	}
}

func tsString(ts int64) string {
	if ts == 0 {
		return ""
	}
	return time.Unix(ts, 0).Format(time.RFC3339)
}
