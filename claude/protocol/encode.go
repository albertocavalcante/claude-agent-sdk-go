package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// UserInput is a user message the host writes to stdin.
type UserInput struct {
	Content Content
	// UUID is the id the CLI echoes back and that rewind_files / per-message
	// cancel refer to. Stamp every message.
	UUID            string
	ParentToolUseID string
}

// EncodeUser encodes a user message stamped with origin {kind: human} and an
// empty session_id (the CLI ignores it and reports the real id on stdout).
func EncodeUser(u UserInput) ([]byte, error) {
	var parent *string
	if u.ParentToolUseID != "" {
		parent = &u.ParentToolUseID
	}
	return json.Marshal(struct {
		Type    string `json:"type"`
		Message struct {
			Role    string  `json:"role"`
			Content Content `json:"content"`
		} `json:"message"`
		ParentToolUseID *string `json:"parent_tool_use_id"`
		SessionID       string  `json:"session_id"`
		UUID            string  `json:"uuid,omitempty"`
		Origin          struct {
			Kind string `json:"kind"`
		} `json:"origin"`
	}{
		Type: "user",
		Message: struct {
			Role    string  `json:"role"`
			Content Content `json:"content"`
		}{"user", u.Content},
		ParentToolUseID: parent,
		UUID:            u.UUID,
		Origin: struct {
			Kind string `json:"kind"`
		}{"human"},
	})
}

// RequestPayload is a typed control request body; ControlSubtype names it.
type RequestPayload interface{ ControlSubtype() string }

// EncodeControlRequest encodes {"type":"control_request","request_id":id,"request":{subtype,...}}.
func EncodeControlRequest(id string, p RequestPayload) ([]byte, error) {
	return encodeRequest(id, p.ControlSubtype(), p)
}

// EncodeRawControlRequest is the escape hatch for subtypes without a typed
// payload. payload must marshal to a JSON object or null.
func EncodeRawControlRequest(id, subtype string, payload any) ([]byte, error) {
	return encodeRequest(id, subtype, payload)
}

func encodeRequest(id, subtype string, payload any) ([]byte, error) {
	var body []byte
	if payload != nil {
		var err error
		if body, err = json.Marshal(payload); err != nil {
			return nil, err
		}
	}
	body = bytes.TrimSpace(body)
	var req bytes.Buffer
	st, _ := json.Marshal(subtype)
	req.WriteString(`{"subtype":`)
	req.Write(st)
	switch {
	case len(body) == 0 || string(body) == "null" || string(body) == "{}":
	case body[0] == '{':
		req.WriteByte(',')
		req.Write(body[1 : len(body)-1])
	default:
		return nil, fmt.Errorf("protocol: control request payload for %q must be a JSON object, got %.20s", subtype, body)
	}
	req.WriteByte('}')
	return json.Marshal(struct {
		Type      string          `json:"type"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}{"control_request", id, req.Bytes()})
}

// EncodeControlSuccess encodes a success control_response for an inbound
// request. payload is AllowResponse, DenyResponse, HookOutput, ... or any
// JSON-marshalable value.
func EncodeControlSuccess(id string, payload any) ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Response  any    `json:"response"`
		} `json:"response"`
	}{Type: "control_response", Response: struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Response  any    `json:"response"`
	}{"success", id, payload}})
}

// EncodeControlError encodes an error control_response.
func EncodeControlError(id, message string) ([]byte, error) {
	return json.Marshal(struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string `json:"subtype"`
			RequestID string `json:"request_id"`
			Error     string `json:"error"`
		} `json:"response"`
	}{Type: "control_response", Response: struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
		Error     string `json:"error"`
	}{"error", id, message}})
}

// HookRegistration registers callbacks for one matcher in initialize.
type HookRegistration struct {
	Matcher         string   `json:"matcher,omitempty"`
	HookCallbackIDs []string `json:"hookCallbackIds"`
	// Timeout is in seconds; zero means the CLI default.
	Timeout int `json:"timeout,omitempty"`
}

// InitializeRequest is the mandatory first control request. Hooks maps a
// hook event name to its registrations; nil encodes as null.
type InitializeRequest struct {
	Hooks  map[string][]HookRegistration `json:"hooks"`
	Agents json.RawMessage               `json:"agents,omitempty"`
}

// InterruptRequest interrupts the running turn. CancelQueued also drops
// queued user messages (capability interrupt_cancel_queued_v1).
type InterruptRequest struct {
	CancelQueued bool `json:"cancel_queued,omitempty"`
}

// SetPermissionModeRequest changes the permission mode.
type SetPermissionModeRequest struct {
	Mode string `json:"mode"`
}

// SetModelRequest changes the model; an empty Model selects the default.
type SetModelRequest struct{ Model string }

// MarshalJSON encodes an empty model as null.
func (s SetModelRequest) MarshalJSON() ([]byte, error) {
	if s.Model == "" {
		return []byte(`{"model":null}`), nil
	}
	return json.Marshal(struct {
		Model string `json:"model"`
	}{s.Model})
}

// MCPStatusRequest asks for the MCP server status.
type MCPStatusRequest struct{}

// ContextUsageRequest asks for context window usage. Detail is "" , "summary" or "full".
type ContextUsageRequest struct {
	Detail string `json:"detail,omitempty"`
}

// RewindFilesRequest rewinds file checkpoints to a user message (needs
// CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=true).
type RewindFilesRequest struct {
	UserMessageID string `json:"user_message_id"`
	DryRun        bool   `json:"dry_run,omitempty"`
}

// StopTaskRequest stops a background task.
type StopTaskRequest struct {
	TaskID string `json:"task_id"`
}

func (*InitializeRequest) ControlSubtype() string        { return "initialize" }
func (*InterruptRequest) ControlSubtype() string         { return "interrupt" }
func (*SetPermissionModeRequest) ControlSubtype() string { return "set_permission_mode" }
func (*SetModelRequest) ControlSubtype() string          { return "set_model" }
func (*MCPStatusRequest) ControlSubtype() string         { return "mcp_status" }
func (*ContextUsageRequest) ControlSubtype() string      { return "get_context_usage" }
func (*RewindFilesRequest) ControlSubtype() string       { return "rewind_files" }
func (*StopTaskRequest) ControlSubtype() string          { return "stop_task" }

// InitializeResponse is the parsed initialize reply. Fields whose shape is
// unstable stay raw.
type InitializeResponse struct {
	Commands              json.RawMessage `json:"commands"`
	Agents                json.RawMessage `json:"agents"`
	Models                json.RawMessage `json:"models"`
	Account               json.RawMessage `json:"account"`
	OutputStyle           string          `json:"output_style"`
	PID                   int             `json:"pid"`
	ClaudeCodeVersion     string          `json:"claude_code_version"`
	CurrentPermissionMode string          `json:"current_permission_mode"`
	Capabilities          []string        `json:"capabilities"`
	SessionState          string          `json:"session_state"`
	SessionID             string          `json:"session_id"`
	Raw                   json.RawMessage `json:"-"`
}

// ParseInitializeResponse decodes an initialize success payload.
func ParseInitializeResponse(raw json.RawMessage) (*InitializeResponse, error) {
	var r InitializeResponse
	if err := unmarshalLoose(raw, &r); err != nil {
		return nil, err
	}
	r.Raw = bytes.Clone(raw)
	return &r, nil
}

// InterruptResponse is the interrupt acknowledgement. It can arrive before
// the interrupted turn's result.
type InterruptResponse struct {
	StillQueued []string `json:"still_queued"`
	Cancelled   []string `json:"cancelled"`
}

// ParseInterruptResponse decodes an interrupt success payload.
func ParseInterruptResponse(raw json.RawMessage) (*InterruptResponse, error) {
	var r InterruptResponse
	if err := unmarshalLoose(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// MCPStatusResponse is the mcp_status reply.
type MCPStatusResponse struct {
	MCPServers []MCPServer `json:"mcpServers"`
}

// ParseMCPStatusResponse decodes an mcp_status success payload.
func ParseMCPStatusResponse(raw json.RawMessage) (*MCPStatusResponse, error) {
	var r MCPStatusResponse
	if err := unmarshalLoose(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// RewindFilesResponse is the rewind_files reply.
type RewindFilesResponse struct {
	CanRewind    bool     `json:"canRewind"`
	Error        string   `json:"error"`
	FilesChanged []string `json:"filesChanged"`
	Insertions   int      `json:"insertions"`
	Deletions    int      `json:"deletions"`
}

// ParseRewindFilesResponse decodes a rewind_files success payload.
func ParseRewindFilesResponse(raw json.RawMessage) (*RewindFilesResponse, error) {
	var r RewindFilesResponse
	if err := unmarshalLoose(raw, &r); err != nil {
		return nil, err
	}
	return &r, nil
}
