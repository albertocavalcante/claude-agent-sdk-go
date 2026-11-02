package protocol

import (
	"bytes"
	"encoding/json"
)

// KeepAlive is a keep_alive frame; consumers ignore it.
type KeepAlive struct{}

func (*KeepAlive) isFrame() {}

// ControlRequest is a request from the CLI to the SDK (can_use_tool,
// hook_callback, elicitation, ...). Request holds the nested request object.
// The same shape appears inside the pending_* arrays of an initialize reply.
type ControlRequest struct {
	RequestID string
	Subtype   string
	Request   json.RawMessage
	Raw       json.RawMessage
}

func (*ControlRequest) isFrame() {}

// ControlCancelRequest asks the SDK to cancel an in-flight inbound request.
// No response is sent for it.
type ControlCancelRequest struct{ RequestID string }

func (*ControlCancelRequest) isFrame() {}

// ControlResponse answers a request the SDK sent. RequestID is the nested
// response.request_id. The pending_* arrays of a re-initialize reply sit on
// the envelope, next to Response, not inside it.
type ControlResponse struct {
	Subtype   string // "success" or "error"
	RequestID string
	Response  json.RawMessage
	Error     string
	ErrorCode string
	// PendingKnown reports whether the CLI included the pending_* fields at
	// all. Absent means an older CLI, not "nothing pending".
	PendingKnown               bool
	PendingPermissionRequests  []*ControlRequest
	PendingElicitationRequests []*ControlRequest
	PendingUserDialogRequests  json.RawMessage
	Raw                        json.RawMessage
}

func (*ControlResponse) isFrame() {}

// IsError reports whether the response is an error response.
func (c *ControlResponse) IsError() bool { return c.Subtype == "error" }

func decodeControlRequest(raw []byte) Frame {
	var v struct {
		RequestID json.RawMessage `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := unmarshalLoose(raw, &v); err != nil {
		return unknown("control_request", raw, err)
	}
	var h struct {
		Subtype string `json:"subtype"`
	}
	_ = unmarshalLoose(v.Request, &h)
	return &ControlRequest{RequestID: rawString(v.RequestID), Subtype: h.Subtype, Request: v.Request, Raw: raw}
}

type pendingAux struct {
	Permission json.RawMessage `json:"pending_permission_requests"`
	Elicit     json.RawMessage `json:"pending_elicitation_requests"`
	UserDialog json.RawMessage `json:"pending_user_dialog_requests"`
}

func decodeControlResponse(raw []byte) Frame {
	type inner struct {
		Subtype   string          `json:"subtype"`
		RequestID json.RawMessage `json:"request_id"`
		Response  json.RawMessage `json:"response"`
		Error     json.RawMessage `json:"error"`
		ErrorCode json.RawMessage `json:"error_code"`
		pendingAux
	}
	var v struct {
		Response *inner `json:"response"`
		inner           // flat envelope fallback
		pendingAux
	}
	if err := unmarshalLoose(raw, &v); err != nil {
		return unknown("control_response", raw, err)
	}
	in := v.inner
	if v.Response != nil && (v.Response.Subtype != "" || len(v.Response.RequestID) > 0) {
		in = *v.Response
	}
	out := &ControlResponse{Subtype: in.Subtype, RequestID: rawString(in.RequestID), Response: in.Response, Error: rawString(in.Error), ErrorCode: rawString(in.ErrorCode), Raw: raw}
	pick := func(outer, nested json.RawMessage) json.RawMessage {
		if len(outer) > 0 {
			return outer
		}
		return nested
	}
	perm := pick(v.Permission, in.Permission)
	elic := pick(v.Elicit, in.Elicit)
	dlg := pick(v.UserDialog, in.UserDialog)
	out.PendingKnown = len(perm) > 0 || len(elic) > 0 || len(dlg) > 0
	out.PendingPermissionRequests = parseRequestList(perm)
	out.PendingElicitationRequests = parseRequestList(elic)
	out.PendingUserDialogRequests = dlg
	return out
}

func parseRequestList(raw json.RawMessage) []*ControlRequest {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil
	}
	var items []json.RawMessage
	if unmarshalLoose(raw, &items) != nil {
		return nil
	}
	var out []*ControlRequest
	for _, it := range items {
		if cr, ok := decodeControlRequest(bytes.Clone(it)).(*ControlRequest); ok && cr.RequestID != "" {
			out = append(out, cr)
		}
	}
	return out
}

// Payload decodes the request into its typed form: *CanUseToolRequest,
// *HookCallbackRequest or *ElicitationRequest. For any other subtype it
// returns (nil, nil); the router must answer such requests with an error
// response.
func (r *ControlRequest) Payload() (any, error) {
	switch r.Subtype {
	case "can_use_tool":
		var p CanUseToolRequest
		if err := unmarshalLoose(r.Request, &p); err != nil {
			return nil, err
		}
		p.Raw = r.Request
		return &p, nil
	case "hook_callback":
		var p HookCallbackRequest
		if err := unmarshalLoose(r.Request, &p); err != nil {
			return nil, err
		}
		p.Raw = r.Request
		return &p, nil
	case "elicitation":
		var p ElicitationRequest
		if err := unmarshalLoose(r.Request, &p); err != nil {
			return nil, err
		}
		p.Raw = r.Request
		return &p, nil
	}
	return nil, nil
}

// CanUseToolRequest asks the host to approve a tool call. It is only sent
// when the CLI runs with --permission-prompt-tool stdio.
type CanUseToolRequest struct {
	ToolName                string             `json:"tool_name"`
	DisplayName             string             `json:"display_name"`
	Input                   json.RawMessage    `json:"input"`
	Description             string             `json:"description"`
	Title                   string             `json:"title"`
	PermissionSuggestions   []PermissionUpdate `json:"permission_suggestions"`
	BlockedPath             string             `json:"blocked_path"`
	DecisionReason          string             `json:"decision_reason"`
	DecisionReasonType      string             `json:"decision_reason_type"`
	DecisionReasonHookEvent string             `json:"decision_reason_hook_event"`
	ToolUseID               string             `json:"tool_use_id"`
	AgentID                 string             `json:"agent_id"`
	MCPServer               json.RawMessage    `json:"mcp_server"`
	Raw                     json.RawMessage    `json:"-"`
}

// HookCallbackRequest runs a registered hook callback. Input is the hook
// input object (hook_event_name, tool_name, tool_input, ...).
type HookCallbackRequest struct {
	CallbackID string          `json:"callback_id"`
	Input      json.RawMessage `json:"input"`
	ToolUseID  string          `json:"tool_use_id"`
	Raw        json.RawMessage `json:"-"`
}

// ElicitationRequest relays an MCP server's elicitation to the host.
type ElicitationRequest struct {
	MCPServerName   string          `json:"mcp_server_name"`
	Message         string          `json:"message"`
	Mode            string          `json:"mode"`
	URL             string          `json:"url"`
	ElicitationID   string          `json:"elicitation_id"`
	RequestedSchema json.RawMessage `json:"requested_schema"`
	Raw             json.RawMessage `json:"-"`
}

// ElicitationResult is the host's answer to an elicitation: Action is
// accept, decline or cancel.
type ElicitationResult struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

// PermissionUpdateType is the "type" of a PermissionUpdate.
type PermissionUpdateType string

// The six permission update types.
const (
	PermAddRules          PermissionUpdateType = "addRules"
	PermReplaceRules      PermissionUpdateType = "replaceRules"
	PermRemoveRules       PermissionUpdateType = "removeRules"
	PermSetMode           PermissionUpdateType = "setMode"
	PermAddDirectories    PermissionUpdateType = "addDirectories"
	PermRemoveDirectories PermissionUpdateType = "removeDirectories"
)

// PermissionRule is one rule of an addRules/replaceRules/removeRules update.
type PermissionRule struct {
	ToolName    string `json:"toolName"`
	RuleContent string `json:"ruleContent,omitempty"`
}

// PermissionUpdate is a tagged permission change. Fill the fields that the
// Type uses: Rules+Behavior for the three rule types, Mode for setMode,
// Directories for the two directory types. Destination is userSettings,
// projectSettings, localSettings, session or cliArg.
type PermissionUpdate struct {
	Type        PermissionUpdateType `json:"type"`
	Rules       []PermissionRule     `json:"rules,omitempty"`
	Behavior    string               `json:"behavior,omitempty"`
	Mode        string               `json:"mode,omitempty"`
	Directories []string             `json:"directories,omitempty"`
	Destination string               `json:"destination,omitempty"`
}

// AllowResponse is the success payload that approves a can_use_tool request.
// UpdatedInput is always sent (the original input when unchanged).
type AllowResponse struct {
	UpdatedInput       json.RawMessage
	UpdatedPermissions []PermissionUpdate
	ToolUseID          string
}

// MarshalJSON encodes {"behavior":"allow",...}.
func (a AllowResponse) MarshalJSON() ([]byte, error) {
	in := a.UpdatedInput
	if len(in) == 0 {
		in = json.RawMessage(`{}`)
	}
	return json.Marshal(struct {
		Behavior           string             `json:"behavior"`
		UpdatedInput       json.RawMessage    `json:"updatedInput"`
		UpdatedPermissions []PermissionUpdate `json:"updatedPermissions,omitempty"`
		ToolUseID          string             `json:"toolUseID,omitempty"`
	}{"allow", in, a.UpdatedPermissions, a.ToolUseID})
}

// DenyResponse is the success payload that denies a can_use_tool request.
// Interrupt aborts the turn.
type DenyResponse struct {
	Message   string
	Interrupt bool
	ToolUseID string
}

// MarshalJSON encodes {"behavior":"deny",...}.
func (d DenyResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Behavior  string `json:"behavior"`
		Message   string `json:"message"`
		Interrupt bool   `json:"interrupt,omitempty"`
		ToolUseID string `json:"toolUseID,omitempty"`
	}{"deny", d.Message, d.Interrupt, d.ToolUseID})
}

// HookOutput is the JSON a hook callback returns. Fields mirror the CLI's
// hook output schema; HookSpecificOutput carries the per-event object
// (for PreToolUse: hookEventName, permissionDecision, ...).
type HookOutput struct {
	Continue           *bool           `json:"continue,omitempty"`
	SuppressOutput     bool            `json:"suppressOutput,omitempty"`
	StopReason         string          `json:"stopReason,omitempty"`
	Decision           string          `json:"decision,omitempty"`
	SystemMessage      string          `json:"systemMessage,omitempty"`
	Reason             string          `json:"reason,omitempty"`
	HookSpecificOutput json.RawMessage `json:"hookSpecificOutput,omitempty"`
	Async              bool            `json:"async,omitempty"`
	AsyncTimeout       int             `json:"asyncTimeout,omitempty"`
}

// rawString renders a JSON value as text: strings unquoted, null as "", any
// other value (number, object) as its JSON. error and error_code are untyped
// on the wire, and request_id must route whatever its type.
func rawString(r json.RawMessage) string {
	if len(r) == 0 || string(r) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(r, &s) == nil {
		return s
	}
	return string(r)
}
