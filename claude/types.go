package claude

import (
	"context"
	"encoding/json"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
)

// UserMessage is a message to send to the CLI. Set UUID to choose the id the
// CLI echoes and that RewindFiles refers to; Send stamps one if empty.
type UserMessage = protocol.UserInput

// UserText returns a plain-text user message.
func UserText(text string) UserMessage { return UserMessage{Content: protocol.TextContent(text)} }

// UserBlocks returns a user message made of content blocks (text, images).
func UserBlocks(blocks ...protocol.Block) UserMessage {
	return UserMessage{Content: protocol.BlocksContent(blocks...)}
}

// ToolPermissionRequest is a can_use_tool request: the CLI asks before
// running ToolName with Input. Read-only Bash and the Agent tool are
// auto-approved by the CLI and never reach the callback.
type ToolPermissionRequest = protocol.CanUseToolRequest

// PermissionUpdate changes permission rules or mode as part of an Allow.
type PermissionUpdate = protocol.PermissionUpdate

// ElicitationRequest is an MCP elicitation relayed by the CLI.
type ElicitationRequest = protocol.ElicitationRequest

// ElicitationResult answers an ElicitationRequest (Action accept, decline or cancel).
type ElicitationResult = protocol.ElicitationResult

// PermissionDecision is Allow or Deny.
type PermissionDecision interface{ isDecision() }

// Allow approves the tool call. UpdatedInput replaces the tool input; nil
// means "unchanged" and the original input is sent back (the CLI requires an
// updatedInput). UpdatedPermissions adds rules or switches mode.
type Allow struct {
	UpdatedInput       any
	UpdatedPermissions []PermissionUpdate
}

// Deny rejects the tool call; Message is shown to the model and Interrupt
// aborts the whole turn.
type Deny struct {
	Message   string
	Interrupt bool
}

func (Allow) isDecision() {}
func (Deny) isDecision()  {}

// HookEvent names a CLI hook event.
type HookEvent string

// Hook events.
const (
	HookPreToolUse         HookEvent = "PreToolUse"
	HookPostToolUse        HookEvent = "PostToolUse"
	HookPostToolUseFailure HookEvent = "PostToolUseFailure"
	HookUserPromptSubmit   HookEvent = "UserPromptSubmit"
	HookStop               HookEvent = "Stop"
	HookSubagentStop       HookEvent = "SubagentStop"
	HookSubagentStart      HookEvent = "SubagentStart"
	HookPreCompact         HookEvent = "PreCompact"
	HookNotification       HookEvent = "Notification"
	HookPermissionRequest  HookEvent = "PermissionRequest"
	HookSessionStart       HookEvent = "SessionStart"
)

// HookInput is what a hook callback receives. Raw holds the complete hook
// input object for fields not modelled here.
type HookInput struct {
	Event          HookEvent
	SessionID      string
	TranscriptPath string
	CWD            string
	PermissionMode string
	ToolName       string
	ToolInput      json.RawMessage
	ToolUseID      string
	AgentID        string
	Raw            json.RawMessage
}

// HookOutput is the hook result; see protocol.HookOutput for the fields.
type HookOutput = protocol.HookOutput

// HookFunc is a hook callback. It runs on its own goroutine; ctx is
// cancelled by control_cancel_request, Close and Detach.
type HookFunc func(ctx context.Context, in HookInput) (HookOutput, error)

// HookMatcher registers callbacks for one matcher (a tool-name pattern such
// as "Bash" or "Edit|Write"; empty matches all). The SDK assigns hook_N ids.
type HookMatcher struct {
	Matcher string
	// Timeout is in seconds; zero uses the CLI default.
	Timeout   int
	Callbacks []HookFunc
}

// UserError is an error whose message may be sent to the CLI (and so shown to
// the model) when a callback fails. Any other callback error is replaced by a
// generic message, so internal details do not leak.
type UserError struct{ Message string }

func (e *UserError) Error() string { return e.Message }

// NewUserError returns a *UserError.
func NewUserError(msg string) error { return &UserError{Message: msg} }
