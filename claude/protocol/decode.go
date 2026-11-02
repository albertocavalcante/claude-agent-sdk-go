package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Default decoder limits.
const (
	DefaultMaxLineBytes = 16 << 20
	DefaultMaxDepth     = 256
)

// Limits bound what Decode accepts. Zero fields take the defaults.
type Limits struct {
	// MaxLineBytes caps one line. Default 16 MiB.
	MaxLineBytes int
	// MaxDepth caps JSON array/object nesting. Default 256.
	MaxDepth int
}

func (l Limits) withDefaults() Limits {
	if l.MaxLineBytes <= 0 {
		l.MaxLineBytes = DefaultMaxLineBytes
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = DefaultMaxDepth
	}
	return l
}

var (
	// ErrSkipLine marks a line that is not a JSON object (blank lines, stray
	// debug output such as "[SandboxDebug] ..."). Callers ignore it.
	ErrSkipLine = errors.New("protocol: not a JSON object line")
	// ErrLineTooLong means the line exceeded Limits.MaxLineBytes.
	ErrLineTooLong = errors.New("protocol: line exceeds size limit")
	// ErrTooDeep means the line nests deeper than Limits.MaxDepth.
	ErrTooDeep = errors.New("protocol: JSON nesting exceeds depth limit")
)

// DecodeError is a malformed JSON line. Raw is truncated to 256 bytes.
type DecodeError struct {
	Err error
	Raw []byte
}

func (e *DecodeError) Error() string {
	return fmt.Sprintf("protocol: decode: %v (line: %.120s)", e.Err, e.Raw)
}
func (e *DecodeError) Unwrap() error { return e.Err }

const rawSnippet = 256

// Decode parses one stdout line into a Frame. Lines that do not start with
// '{' yield ErrSkipLine. A line over the size cap yields ErrLineTooLong and a
// too-deeply nested one ErrTooDeep; both are rejected before any JSON
// parsing. A '{' line that is not valid JSON yields *DecodeError. Unknown
// types and fields are not errors.
func Decode(line []byte, lim Limits) (Frame, error) {
	lim = lim.withDefaults()
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return nil, ErrSkipLine
	}
	if len(line) > lim.MaxLineBytes {
		return nil, ErrLineTooLong
	}
	if exceedsDepth(line, lim.MaxDepth) {
		return nil, ErrTooDeep
	}
	raw := bytes.Clone(line)
	var env struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		var te *json.UnmarshalTypeError
		if !errors.As(err, &te) { // a non-string "type" is tolerated below
			return nil, &DecodeError{Err: err, Raw: bytes.Clone(raw[:min(len(raw), rawSnippet)])}
		}
	}
	return decodeTyped(env.Type, raw), nil
}

// exceedsDepth is a single cheap pass over the bytes that tracks string and
// escape state and reports whether bracket nesting passes max.
func exceedsDepth(b []byte, maxDepth int) bool {
	depth := 0
	inStr, esc := false, false
	for _, c := range b {
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{', '[':
			depth++
			if depth > maxDepth {
				return true
			}
		case '}', ']':
			depth--
		}
	}
	return false
}

func unknown(typ string, raw []byte, err error) *Unknown {
	return &Unknown{Type: typ, Err: err, Raw: raw}
}

func decodeTyped(typ string, raw []byte) Frame {
	switch typ {
	case "assistant":
		return decodeAssistant(raw)
	case "user":
		return decodeUser(raw)
	case "system":
		return decodeSystem(raw)
	case "stream_event":
		var v struct {
			UUID            string          `json:"uuid"`
			SessionID       string          `json:"session_id"`
			ParentToolUseID string          `json:"parent_tool_use_id"`
			Event           json.RawMessage `json:"event"`
		}
		if err := unmarshalLoose(raw, &v); err != nil {
			return unknown(typ, raw, err)
		}
		var ev struct {
			Type string `json:"type"`
		}
		_ = unmarshalLoose(v.Event, &ev)
		return &StreamEvent{UUID: v.UUID, SessionID: v.SessionID, ParentToolUseID: v.ParentToolUseID, EventType: ev.Type, Event: v.Event, Raw: raw}
	case "result":
		return decodeResult(raw)
	case "rate_limit_event":
		var v struct {
			Info json.RawMessage `json:"rate_limit_info"`
		}
		if err := unmarshalLoose(raw, &v); err != nil {
			return unknown(typ, raw, err)
		}
		var info struct {
			Status        string `json:"status"`
			RateLimitType string `json:"rateLimitType"`
		}
		_ = unmarshalLoose(v.Info, &info)
		return &RateLimitEvent{Status: info.Status, RateLimitType: info.RateLimitType, Info: v.Info, Raw: raw}
	case "conversation_reset":
		var v struct {
			NewConversationID string `json:"new_conversation_id"`
		}
		if err := unmarshalLoose(raw, &v); err != nil {
			return unknown(typ, raw, err)
		}
		return &ConversationReset{NewConversationID: v.NewConversationID, Raw: raw}
	case "keep_alive":
		return &KeepAlive{}
	case "control_request":
		return decodeControlRequest(raw)
	case "control_response":
		return decodeControlResponse(raw)
	case "control_cancel_request":
		var v struct {
			RequestID json.RawMessage `json:"request_id"`
		}
		if err := unmarshalLoose(raw, &v); err != nil {
			return unknown(typ, raw, err)
		}
		return &ControlCancelRequest{RequestID: rawString(v.RequestID)}
	}
	return unknown(typ, raw, nil)
}

type assistantBodyAux struct {
	ID         string            `json:"id"`
	Model      string            `json:"model"`
	Role       string            `json:"role"`
	StopReason string            `json:"stop_reason"`
	Content    []json.RawMessage `json:"content"`
	Usage      json.RawMessage   `json:"usage"`
}

func (a assistantBodyAux) body() AssistantBody {
	b := AssistantBody{ID: a.ID, Model: a.Model, Role: a.Role, StopReason: a.StopReason, Usage: a.Usage}
	for _, c := range a.Content {
		b.Content = append(b.Content, ParseBlock(c))
	}
	return b
}

func decodeAssistant(raw []byte) Frame {
	var v struct {
		UUID            string            `json:"uuid"`
		SessionID       string            `json:"session_id"`
		ParentToolUseID string            `json:"parent_tool_use_id"`
		Timestamp       string            `json:"timestamp"`
		Error           string            `json:"error"`
		Aborted         bool              `json:"aborted"`
		Message         *assistantBodyAux `json:"message"`
		// Flat envelope: the body fields at top level.
		assistantBodyAux
	}
	if err := unmarshalLoose(raw, &v); err != nil {
		return unknown("assistant", raw, err)
	}
	aux := v.assistantBodyAux
	if v.Message != nil { // nested wins over flat
		aux = *v.Message
	}
	return &Assistant{UUID: v.UUID, SessionID: v.SessionID, ParentToolUseID: v.ParentToolUseID, Timestamp: v.Timestamp,
		Error: v.Error, Aborted: v.Aborted, Message: aux.body(), Raw: raw}
}

func decodeUser(raw []byte) Frame {
	var v struct {
		UUID            string          `json:"uuid"`
		SessionID       string          `json:"session_id"`
		ParentToolUseID string          `json:"parent_tool_use_id"`
		Timestamp       string          `json:"timestamp"`
		IsReplay        bool            `json:"isReplay"`
		IsSynthetic     bool            `json:"isSynthetic"`
		Origin          json.RawMessage `json:"origin"`
		ToolUseResult   json.RawMessage `json:"tool_use_result"`
		ToolResultMeta  json.RawMessage `json:"tool_result_meta"`
		Message         *struct {
			Role    string  `json:"role"`
			Content Content `json:"content"`
		} `json:"message"`
		Content Content `json:"content"` // flat envelope
	}
	if err := unmarshalLoose(raw, &v); err != nil {
		return unknown("user", raw, err)
	}
	body := UserBody{Role: "user", Content: v.Content}
	if v.Message != nil {
		body = UserBody{Role: v.Message.Role, Content: v.Message.Content}
	}
	return &User{UUID: v.UUID, SessionID: v.SessionID, ParentToolUseID: v.ParentToolUseID, Timestamp: v.Timestamp,
		IsReplay: v.IsReplay, IsSynthetic: v.IsSynthetic, Origin: v.Origin, ToolUseResult: v.ToolUseResult,
		ToolResultMeta: v.ToolResultMeta, Message: body, Raw: raw}
}

func decodeSystem(raw []byte) Frame {
	var v struct {
		Subtype           string      `json:"subtype"`
		UUID              string      `json:"uuid"`
		SessionID         string      `json:"session_id"`
		CWD               string      `json:"cwd"`
		Model             string      `json:"model"`
		PermissionMode    string      `json:"permissionMode"`
		Tools             []string    `json:"tools"`
		MCPServers        []MCPServer `json:"mcp_servers"`
		Capabilities      []string    `json:"capabilities"`
		ClaudeCodeVersion string      `json:"claude_code_version"`
		APIKeySource      string      `json:"apiKeySource"`
		Status            string      `json:"status"`
		State             string      `json:"state"`
		SDKHostOnly       bool        `json:"sdk_host_only"`
		CompactMetadata   struct {
			Trigger    string `json:"trigger"`
			PreTokens  int    `json:"pre_tokens"`
			PostTokens int    `json:"post_tokens"`
		} `json:"compact_metadata"`
	}
	su := &SystemUnknown{Raw: raw}
	if err := unmarshalLoose(raw, &v); err != nil {
		// Some field has an unexpected type; recover what the subtype needs.
		var h struct {
			Subtype   string `json:"subtype"`
			UUID      string `json:"uuid"`
			SessionID string `json:"session_id"`
		}
		_ = unmarshalLoose(raw, &h)
		su.SubtypeName, su.UUID, su.SessionID = h.Subtype, h.UUID, h.SessionID
		return su
	}
	switch v.Subtype {
	case "init":
		return &SystemInit{UUID: v.UUID, SessionID: v.SessionID, CWD: v.CWD, Model: v.Model, PermissionMode: v.PermissionMode,
			Tools: v.Tools, MCPServers: v.MCPServers, Capabilities: v.Capabilities, ClaudeCodeVersion: v.ClaudeCodeVersion,
			APIKeySource: v.APIKeySource, Raw: raw}
	case "status":
		return &SystemStatus{UUID: v.UUID, SessionID: v.SessionID, Status: v.Status, PermissionMode: v.PermissionMode, Raw: raw}
	case "compact_boundary":
		return &CompactBoundary{UUID: v.UUID, SessionID: v.SessionID, Trigger: v.CompactMetadata.Trigger,
			PreTokens: v.CompactMetadata.PreTokens, PostTokens: v.CompactMetadata.PostTokens, Raw: raw}
	case "session_state_changed":
		return &SessionStateChanged{UUID: v.UUID, SessionID: v.SessionID, State: v.State, SDKHostOnly: v.SDKHostOnly, Raw: raw}
	}
	su.SubtypeName, su.UUID, su.SessionID = v.Subtype, v.UUID, v.SessionID
	return su
}

func decodeResult(raw []byte) Frame {
	var v struct {
		Subtype                   string                `json:"subtype"`
		IsError                   bool                  `json:"is_error"`
		Result                    string                `json:"result"`
		StopReason                string                `json:"stop_reason"`
		TerminalReason            string                `json:"terminal_reason"`
		SessionID                 string                `json:"session_id"`
		UUID                      string                `json:"uuid"`
		NumTurns                  int                   `json:"num_turns"`
		DurationMS                float64               `json:"duration_ms"`
		DurationAPIMS             float64               `json:"duration_api_ms"`
		TotalCostUSD              float64               `json:"total_cost_usd"`
		Usage                     Usage                 `json:"usage"`
		ModelUsage                map[string]ModelUsage `json:"modelUsage"`
		ModelUsageSinceLastResult map[string]ModelUsage `json:"model_usage_since_last_result"`
		PermissionDenials         json.RawMessage       `json:"permission_denials"`
		Errors                    []string              `json:"errors"`
		APIErrorStatus            json.RawMessage       `json:"api_error_status"`
		StructuredOutput          json.RawMessage       `json:"structured_output"`
		DeferredToolUse           json.RawMessage       `json:"deferred_tool_use"`
		QueuedTurnCount           int                   `json:"queued_turn_count"`
		ResultIndex               int                   `json:"result_index"`
	}
	if err := unmarshalLoose(raw, &v); err != nil {
		return unknown("result", raw, err)
	}
	return &Result{Subtype: v.Subtype, IsError: v.IsError, Result: v.Result, StopReason: v.StopReason,
		TerminalReason: v.TerminalReason, SessionID: v.SessionID, UUID: v.UUID, NumTurns: v.NumTurns,
		DurationMS: v.DurationMS, DurationAPIMS: v.DurationAPIMS, TotalCostUSD: v.TotalCostUSD, Usage: v.Usage,
		ModelUsage: v.ModelUsage, ModelUsageSinceLastResult: v.ModelUsageSinceLastResult,
		PermissionDenials: v.PermissionDenials, Errors: v.Errors, APIErrorStatus: v.APIErrorStatus,
		StructuredOutput: v.StructuredOutput, DeferredToolUse: v.DeferredToolUse,
		QueuedTurnCount: v.QueuedTurnCount, ResultIndex: v.ResultIndex, Raw: raw}
}

// unmarshalLoose is json.Unmarshal that tolerates field type drift: a value
// of the wrong type leaves that field zero (encoding/json carries on with the
// rest) and is not an error. Only malformed JSON fails. This keeps a CLI that
// changes one field's type from downgrading a result, init or request.
func unmarshalLoose(data []byte, v any) error {
	err := json.Unmarshal(data, v)
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) {
		return nil
	}
	return err
}
