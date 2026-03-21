package claude

import "encoding/json"

// Message is the interface implemented by all message types streamed
// from the Claude CLI.
type Message interface {
	// Type returns the message type identifier
	// (e.g. "assistant", "user", "result", "system").
	Type() string
}

// AssistantMessage represents a response from the model.
type AssistantMessage struct {
	Content    []ContentBlock `json:"-"`
	Model      string         `json:"model,omitempty"`
	StopReason string         `json:"stop_reason,omitempty"`
}

// Type returns "assistant".
func (m *AssistantMessage) Type() string { return "assistant" }

// UserMessage represents a user turn in the conversation.
type UserMessage struct {
	Content []ContentBlock `json:"-"`
}

// Type returns "user".
func (m *UserMessage) Type() string { return "user" }

// ResultMessage represents the final result of a CLI invocation,
// including usage statistics.
type ResultMessage struct {
	IsError      bool    `json:"is_error,omitempty"`
	Duration     float64 `json:"duration_ms,omitempty"`
	Cost         float64 `json:"cost_usd,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	NumTurns     int     `json:"num_turns,omitempty"`
}

// Type returns "result".
func (m *ResultMessage) Type() string { return "result" }

// SystemMessage represents a system-level event from the CLI
// (e.g. initialization, compaction).
type SystemMessage struct {
	Subtype string          `json:"subtype,omitempty"`
	Raw     json.RawMessage `json:"-"`
}

// Type returns "system".
func (m *SystemMessage) Type() string { return "system" }

// UnknownMessage represents any message type the SDK does not recognize.
// It is returned instead of an error to ensure forward compatibility.
type UnknownMessage struct {
	RawType string          `json:"-"`
	Raw     json.RawMessage `json:"-"`
}

// Type returns the raw type string from the JSON message.
func (m *UnknownMessage) Type() string { return m.RawType }
