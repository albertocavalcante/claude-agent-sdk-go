package protocol

import (
	"encoding/json"
	"strings"
)

// Frame is anything that can arrive on the CLI's stdout: a Message or one
// of the control frames.
type Frame interface{ isFrame() }

// Message is a stdout message a consumer reads. The set is sealed: the
// concrete types are *Assistant, *User, *SystemInit, *SystemStatus,
// *CompactBoundary, *SessionStateChanged, *SystemUnknown, *StreamEvent,
// *Result, *RateLimitEvent, *ConversationReset and *Unknown.
type Message interface {
	Frame
	// MessageType is the wire "type" (for system messages, "system").
	MessageType() string
	// RawJSON is the original line, byte for byte.
	RawJSON() json.RawMessage
	isMessage()
}

// Assistant is one assistant frame. A single API message is split into
// several frames, one per content block, all sharing Message.ID.
type Assistant struct {
	UUID            string
	SessionID       string
	ParentToolUseID string // non-empty for sub-agent traffic
	Timestamp       string
	Message         AssistantBody
	Error           string
	Aborted         bool // the frame was cut short by an interrupt
	Raw             json.RawMessage
}

// AssistantBody is the Anthropic Message inside an Assistant frame.
// StopReason is empty in streamed frames; use the stream events or
// Result.StopReason.
type AssistantBody struct {
	ID         string
	Model      string
	Role       string
	StopReason string
	Content    []Block
	Usage      json.RawMessage
}

// Text concatenates the text blocks of the frame.
func (a *Assistant) Text() string {
	var sb strings.Builder
	for _, b := range a.Message.Content {
		if t, ok := b.(*TextBlock); ok {
			sb.WriteString(t.Text)
		}
	}
	return sb.String()
}

// UserBody is the message inside a User frame.
type UserBody struct {
	Role    string
	Content Content
}

// User is a user frame on stdout: tool results, synthetic text such as
// "[Request interrupted by user]", or (with replay enabled) the echo of a
// message the host wrote.
type User struct {
	UUID            string
	SessionID       string
	ParentToolUseID string
	Timestamp       string
	Message         UserBody
	IsReplay        bool
	IsSynthetic     bool
	Origin          json.RawMessage
	ToolUseResult   json.RawMessage
	ToolResultMeta  json.RawMessage
	Raw             json.RawMessage
}

// MCPServer describes one MCP server in system/init or an mcp_status reply.
type MCPServer struct {
	Name       string          `json:"name"`
	Status     string          `json:"status"`
	Source     string          `json:"source,omitempty"`
	Scope      string          `json:"scope,omitempty"`
	Error      string          `json:"error,omitempty"`
	ServerInfo json.RawMessage `json:"serverInfo,omitempty"`
	Config     json.RawMessage `json:"config,omitempty"`
	Tools      json.RawMessage `json:"tools,omitempty"`
}

// SystemInit is system/init, emitted at the start of each turn. Its
// SessionID is authoritative; it changes after a conversation reset.
type SystemInit struct {
	UUID              string
	SessionID         string
	CWD               string
	Model             string
	PermissionMode    string
	Tools             []string
	MCPServers        []MCPServer
	Capabilities      []string
	ClaudeCodeVersion string
	APIKeySource      string
	Raw               json.RawMessage
}

// SystemStatus is system/status. Status is "" (wire null), "requesting" or
// "compacting".
type SystemStatus struct {
	UUID           string
	SessionID      string
	Status         string
	PermissionMode string
	Raw            json.RawMessage
}

// CompactBoundary is system/compact_boundary.
type CompactBoundary struct {
	UUID       string
	SessionID  string
	Trigger    string
	PreTokens  int
	PostTokens int
	Raw        json.RawMessage
}

// SessionStateChanged is system/session_state_changed (only emitted with
// CLAUDE_CODE_SDK_READS_SESSION_STATE=1). State is running, requires_action
// or idle; idle after a result means the run is over.
type SessionStateChanged struct {
	UUID        string
	SessionID   string
	State       string
	SDKHostOnly bool
	Raw         json.RawMessage
}

// SystemUnknown is any other system subtype (task_started, api_retry, ...).
type SystemUnknown struct {
	SubtypeName string
	UUID        string
	SessionID   string
	Raw         json.RawMessage
}

// StreamEvent wraps a raw Anthropic SSE event (needs IncludePartialMessages).
type StreamEvent struct {
	UUID            string
	SessionID       string
	ParentToolUseID string
	EventType       string
	Event           json.RawMessage
	Raw             json.RawMessage
}

// TextDelta returns the text of a content_block_delta/text_delta event.
func (s *StreamEvent) TextDelta() (string, bool) {
	if s.EventType != "content_block_delta" {
		return "", false
	}
	var ev struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if json.Unmarshal(s.Event, &ev) != nil || ev.Delta.Type != "text_delta" {
		return "", false
	}
	return ev.Delta.Text, true
}

// Usage is per-turn token usage.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// ModelUsage is per-model usage. In Result.ModelUsage it is cumulative for
// the process; in ModelUsageSinceLastResult it is the per-turn delta.
type ModelUsage struct {
	InputTokens              int     `json:"inputTokens"`
	OutputTokens             int     `json:"outputTokens"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens"`
	WebSearchRequests        int     `json:"webSearchRequests"`
	CostUSD                  float64 `json:"costUSD"`
	ContextWindow            int     `json:"contextWindow"`
	MaxOutputTokens          int     `json:"maxOutputTokens"`
}

// Result ends a turn. Usage is per turn, while TotalCostUSD and ModelUsage
// are cumulative for the process (they reset on a conversation reset).
type Result struct {
	Subtype                   string
	IsError                   bool
	Result                    string
	StopReason                string
	TerminalReason            string
	SessionID                 string
	UUID                      string
	NumTurns                  int
	DurationMS                float64
	DurationAPIMS             float64
	TotalCostUSD              float64
	Usage                     Usage
	ModelUsage                map[string]ModelUsage
	ModelUsageSinceLastResult map[string]ModelUsage
	PermissionDenials         json.RawMessage
	Errors                    []string
	APIErrorStatus            json.RawMessage
	StructuredOutput          json.RawMessage
	DeferredToolUse           json.RawMessage
	QueuedTurnCount           int
	ResultIndex               int
	Raw                       json.RawMessage
}

// RateLimitEvent is rate_limit_event.
type RateLimitEvent struct {
	Status        string
	RateLimitType string
	Info          json.RawMessage
	Raw           json.RawMessage
}

// ConversationReset announces a conversation reset (/clear); the session id
// becomes NewConversationID.
type ConversationReset struct {
	NewConversationID string
	Raw               json.RawMessage
}

// Unknown is any top-level message type this package does not model.
type Unknown struct {
	Type string
	// Err is set when a known type failed to decode into its typed form; the
	// message is then surfaced as Unknown with Raw intact.
	Err error
	Raw json.RawMessage
}

func (*Assistant) MessageType() string           { return "assistant" }
func (*User) MessageType() string                { return "user" }
func (*SystemInit) MessageType() string          { return "system" }
func (*SystemStatus) MessageType() string        { return "system" }
func (*CompactBoundary) MessageType() string     { return "system" }
func (*SessionStateChanged) MessageType() string { return "system" }
func (*SystemUnknown) MessageType() string       { return "system" }
func (*StreamEvent) MessageType() string         { return "stream_event" }
func (*Result) MessageType() string              { return "result" }
func (*RateLimitEvent) MessageType() string      { return "rate_limit_event" }
func (*ConversationReset) MessageType() string   { return "conversation_reset" }
func (u *Unknown) MessageType() string           { return u.Type }

func (m *Assistant) RawJSON() json.RawMessage           { return m.Raw }
func (m *User) RawJSON() json.RawMessage                { return m.Raw }
func (m *SystemInit) RawJSON() json.RawMessage          { return m.Raw }
func (m *SystemStatus) RawJSON() json.RawMessage        { return m.Raw }
func (m *CompactBoundary) RawJSON() json.RawMessage     { return m.Raw }
func (m *SessionStateChanged) RawJSON() json.RawMessage { return m.Raw }
func (m *SystemUnknown) RawJSON() json.RawMessage       { return m.Raw }
func (m *StreamEvent) RawJSON() json.RawMessage         { return m.Raw }
func (m *Result) RawJSON() json.RawMessage              { return m.Raw }
func (m *RateLimitEvent) RawJSON() json.RawMessage      { return m.Raw }
func (m *ConversationReset) RawJSON() json.RawMessage   { return m.Raw }
func (m *Unknown) RawJSON() json.RawMessage             { return m.Raw }

// Subtype returns the system subtype for the system message types.
func (*SystemInit) Subtype() string          { return "init" }
func (*SystemStatus) Subtype() string        { return "status" }
func (*CompactBoundary) Subtype() string     { return "compact_boundary" }
func (*SessionStateChanged) Subtype() string { return "session_state_changed" }
func (s *SystemUnknown) Subtype() string     { return s.SubtypeName }

func (*Assistant) isFrame()           {}
func (*User) isFrame()                {}
func (*SystemInit) isFrame()          {}
func (*SystemStatus) isFrame()        {}
func (*CompactBoundary) isFrame()     {}
func (*SessionStateChanged) isFrame() {}
func (*SystemUnknown) isFrame()       {}
func (*StreamEvent) isFrame()         {}
func (*Result) isFrame()              {}
func (*RateLimitEvent) isFrame()      {}
func (*ConversationReset) isFrame()   {}
func (*Unknown) isFrame()             {}

func (*Assistant) isMessage()           {}
func (*User) isMessage()                {}
func (*SystemInit) isMessage()          {}
func (*SystemStatus) isMessage()        {}
func (*CompactBoundary) isMessage()     {}
func (*SessionStateChanged) isMessage() {}
func (*SystemUnknown) isMessage()       {}
func (*StreamEvent) isMessage()         {}
func (*Result) isMessage()              {}
func (*RateLimitEvent) isMessage()      {}
func (*ConversationReset) isMessage()   {}
func (*Unknown) isMessage()             {}
