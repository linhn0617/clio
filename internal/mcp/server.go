// Package mcp exposes clio's index to Claude Code over the stdio MCP protocol.
package mcp

import (
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/linhn0617/clio/internal/db"
	"github.com/linhn0617/clio/internal/registry"
)

// sourceDescription builds the "<prefix>: claude-code (default), codex, or
// all"-style prose used by a tool's "source" parameter description: every
// registered source name (registry.Names(), in Seed order, the default
// source marked "(default)") plus the "all" filter value. Generated from the
// registry rather than a hardcoded per-tool sentence, so a source added to
// the registry appears in every tool's description without editing this
// file (codex review P1 finding #1).
func sourceDescription(prefix string) string {
	def := registry.DefaultSource()
	names := registry.Names()
	parts := make([]string, len(names))
	for i, n := range names {
		if n == def {
			parts[i] = n + " (default)"
		} else {
			parts[i] = n
		}
	}
	return prefix + ": " + strings.Join(parts, ", ") + ", or " + registry.All
}

// sourceOptions returns the "source" parameter's description, enum, and
// default, all derived from the source registry rather than hardcoded per
// tool (design.md D5): one helper feeds all five read tools, so a source
// added to the registry appears in every tool's enum and description
// without editing this file. prefix is the tool-specific lead-in
// (e.g. "Which tool's history"); sourceDescription appends the registry-
// derived name list.
func sourceOptions(prefix string) []mcp.PropertyOption {
	return []mcp.PropertyOption{
		mcp.Description(sourceDescription(prefix)),
		mcp.Enum(registry.EnumValues()...),
		mcp.DefaultString(registry.DefaultSource()),
	}
}

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
		mcp.WithString("source", sourceOptions("Which tool's history")...),
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
		mcp.WithNumber("limit", mcp.Description(fmt.Sprintf("Max sessions in the bundle (default %d, max %d)", defaultAskSessions, maxAskSessions)), mcp.DefaultNumber(defaultAskSessions), mcp.Min(1), mcp.Max(maxAskSessions)),
		mcp.WithNumber("max_tokens", mcp.Description(fmt.Sprintf("Max estimated tokens in the bundle's excerpt text (default %d, min %d, max %d)", defaultMaxTokens, minMaxTokens, maxMaxTokens)), mcp.DefaultNumber(defaultMaxTokens), mcp.Min(minMaxTokens), mcp.Max(maxMaxTokens)),
		mcp.WithString("source", sourceOptions("Which tool's history")...),
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
		mcp.WithBoolean("include_subagents", mcp.Description("Include subagent child sessions (default: top-level only)"), mcp.DefaultBool(false)),
		mcp.WithString("source", sourceOptions("Which tool's history")...),
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
		mcp.WithString("source", sourceOptions("Which tool's history")...),
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
		mcp.WithBoolean("include_subagents", mcp.Description("Inline each subagent's messages in the result")),
		mcp.WithString("source", sourceOptions("Source filter for prefix resolution")...),
	), handleReadSession(database, beforeRead))

	return s
}

// Serve runs the server over stdio, blocking until the client disconnects.
func Serve(s *server.MCPServer) error {
	return server.ServeStdio(s)
}
