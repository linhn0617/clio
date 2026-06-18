// Package model holds clio's domain types and the raw Claude Code event shapes.
package model

import "encoding/json"

// Roles stored in the messages table.
const (
	RoleUser       = "user"
	RoleAssistant  = "assistant"
	RoleSystem     = "system"
	RoleThinking   = "thinking"
	RoleToolUse    = "tool_use"
	RoleToolResult = "tool_result"
)

// Session is one Claude Code conversation file.
type Session struct {
	UUID          string
	ProjectPath   string
	SourceFile    string
	StartedAt     int64
	EndedAt       int64
	TurnCount     int
	Title         string
	ParentSession string
	AgentType     string
}

// Message is one indexable unit derived from an event's content block.
type Message struct {
	SessionUUID string
	Seq         int
	TS          int64
	Role        string
	Content     string
	RawJSON     string
	ToolCalls   []ToolCall
	Targets     []ToolTarget
}

// ToolCall records a tool invocation's name and a short summary.
type ToolCall struct {
	ToolName      string
	ParamsSummary string
}

// ToolTarget is one structured activity fact extracted from a tool call: the
// tool used, or a file/command/pattern/url it acted on.
type ToolTarget struct {
	Kind  string
	Value string
}

// Activity fact kinds stored in tool_targets.
const (
	TargetFile    = "file"
	TargetCommand = "command"
	TargetPattern = "pattern"
	TargetURL     = "url"
	TargetTool    = "tool"
)

// RawEvent is a single line of a .jsonl session file. Only fields clio uses are
// declared; unknown fields are ignored for forward compatibility.
type RawEvent struct {
	Type             string      `json:"type"`
	UUID             string      `json:"uuid"`
	ParentUUID       *string     `json:"parentUuid"`
	Timestamp        string      `json:"timestamp"`
	CWD              string      `json:"cwd"`
	SessionID        string      `json:"sessionId"`
	AttributionAgent string      `json:"attributionAgent"`
	Message          *RawMessage `json:"message"`
	Summary          string      `json:"summary"`
}

// RawMessage is the nested message object on user/assistant events.
type RawMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ContentBlock is one element of a message's content array. A message's content
// may also be a bare JSON string, handled separately by the parser.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`          // tool_use
	Name      string          `json:"name"`        // tool_use
	Input     json.RawMessage `json:"input"`       // tool_use
	ToolUseID string          `json:"tool_use_id"` // tool_result
	Content   json.RawMessage `json:"content"`     // tool_result (string or array)
}
