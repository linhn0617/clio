// Package mcp exposes clio's index to Claude Code over the stdio MCP protocol.
package mcp

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/linhn0617/clio/internal/db"
)

// NewServer builds an MCP server with clio's four tools registered. beforeRead,
// if non-nil, runs before each read tool serves (used by followers for a
// best-effort catch-up). It must never block indefinitely or panic.
func NewServer(database *db.DB, version string, beforeRead func()) *server.MCPServer {
	s := server.NewMCPServer(
		"clio", version,
		server.WithToolCapabilities(false),
		server.WithRecovery(),
		server.WithInstructions("clio exposes your Claude Code conversation history. Use `ask` to get a cited bundle of past excerpts answering a question (then synthesize the answer yourself), `search` to find past discussions, `read_session` to read one in full, `list_sessions` to browse, and `activity_summary` for what you worked on over a period."),
	)

	// All five tools query the local session index — read-only, non-destructive,
	// non-open-world. Without these annotations, MCP clients default to the
	// least-safe assumption (destructive + open-world) and surface scary tags.
	s.AddTool(mcp.NewTool("search",
		mcp.WithDescription("Full-text search across all past Claude Code conversations. Returns ranked snippets; tool output is excluded unless include_tool_output is true."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search terms; supports phrases in double quotes")),
		mcp.WithString("since", mcp.Description("Only results since this time: 7d, 12h, 30m, or YYYY-MM-DD")),
		mcp.WithString("project", mcp.Description("Filter by project path prefix")),
		mcp.WithString("role", mcp.Description("Filter by role"), mcp.Enum("user", "assistant")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 10, max 50)"), mcp.DefaultNumber(defaultSearchLimit), mcp.Min(1), mcp.Max(maxSearchLimit)),
		mcp.WithBoolean("include_tool_output", mcp.Description("Include tool output in results"), mcp.DefaultBool(false)),
	), handleSearch(database, beforeRead))

	s.AddTool(mcp.NewTool("ask",
		mcp.WithDescription("Answer a question from past conversations. Returns a cited evidence bundle — the most relevant excerpts, each with a window of surrounding turns, grouped by session. clio does not generate an answer; synthesize it yourself from the excerpts and cite the session ids."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("question", mcp.Required(), mcp.Description("The natural-language question to answer from history")),
		mcp.WithString("since", mcp.Description("Only consider sessions since: 7d, 12h, 30m, or YYYY-MM-DD")),
		mcp.WithString("project", mcp.Description("Limit to a project path prefix (default: all projects)")),
		mcp.WithNumber("limit", mcp.Description("Max sessions in the bundle (default 6, max 10)"), mcp.DefaultNumber(defaultAskSessions), mcp.Min(1), mcp.Max(maxAskSessions)),
	), handleAsk(database, beforeRead))

	s.AddTool(mcp.NewTool("list_sessions",
		mcp.WithDescription("List past sessions, most recent first, with optional filters."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("since", mcp.Description("Only sessions since: 7d, 12h, 30m, or YYYY-MM-DD")),
		mcp.WithString("project", mcp.Description("Filter by project path prefix")),
		mcp.WithNumber("min_turns", mcp.Description("Only sessions with at least this many user turns"), mcp.DefaultNumber(0)),
		mcp.WithNumber("limit", mcp.Description("Max sessions (default 20, max 50)"), mcp.DefaultNumber(20), mcp.Min(1), mcp.Max(maxSearchLimit)),
		mcp.WithString("touched", mcp.Description("Only sessions whose tool calls touched this path prefix")),
		mcp.WithString("tool", mcp.Description("Only sessions that used this tool, e.g. Bash or mcp__server__name")),
		mcp.WithString("ran", mcp.Description("Only sessions that ran a command containing this substring")),
	), handleListSessions(database, beforeRead))

	s.AddTool(mcp.NewTool("activity_summary",
		mcp.WithDescription("Summarize activity over a period. Group by day/project for session and message counts ('what did I work on last week?'), or by file/command/tool/pattern/url for the most frequent files touched, commands run, or tools used."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("since", mcp.Description("Period start: 7d, 12h, 30m, or YYYY-MM-DD (default 7d)")),
		mcp.WithString("group_by", mcp.Description("Grouping: day or project (counts), or file/command/tool/pattern/url (activity)"), mcp.Enum("day", "project", "file", "command", "tool", "pattern", "url"), mcp.DefaultString("day")),
		mcp.WithString("project", mcp.Description("Filter by project path prefix")),
		mcp.WithNumber("limit", mcp.Description("Max rows for file/command/tool/pattern/url grouping (default 30, max 50)"), mcp.DefaultNumber(30), mcp.Min(1), mcp.Max(maxSearchLimit)),
	), handleActivitySummary(database, beforeRead))

	s.AddTool(mcp.NewTool("read_session",
		mcp.WithDescription("Read a session's messages in full, paginated. Tool output is excluded unless include_tool_output is true."),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithIdempotentHintAnnotation(true),
		mcp.WithOpenWorldHintAnnotation(false),
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Session uuid or unambiguous prefix")),
		mcp.WithNumber("offset", mcp.Description("Message offset for pagination"), mcp.DefaultNumber(0), mcp.Min(0)),
		mcp.WithNumber("limit", mcp.Description("Max messages per page (default 50, max 200)"), mcp.DefaultNumber(defaultReadLimit), mcp.Min(1), mcp.Max(maxReadLimit)),
		mcp.WithBoolean("include_tool_output", mcp.Description("Include tool output / thinking"), mcp.DefaultBool(false)),
	), handleReadSession(database, beforeRead))

	return s
}

// Serve runs the server over stdio, blocking until the client disconnects.
func Serve(s *server.MCPServer) error {
	return server.ServeStdio(s)
}
