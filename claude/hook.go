package claude

import (
	"context"
	"encoding/json"
	"regexp"
)

// HookEvent identifies when a hook fires.
type HookEvent string

const (
	// HookPreToolUse fires when an AssistantMessage contains a ToolUseBlock,
	// before the tool is executed.
	HookPreToolUse HookEvent = "PreToolUse"

	// HookPostToolUse fires when a UserMessage contains a ToolResultBlock,
	// after the tool has executed.
	HookPostToolUse HookEvent = "PostToolUse"

	// HookMessage fires for every message received from the CLI.
	HookMessage HookEvent = "Message"

	// HookResult fires when a ResultMessage is received.
	HookResult HookEvent = "Result"
)

// HookRegistration binds a callback to an event.
type HookRegistration struct {
	// Event is the hook event to listen for.
	Event HookEvent

	// ToolPattern is an optional regex filter on the tool name.
	// If empty, the hook fires for all tools. Only applies to
	// PreToolUse and PostToolUse events.
	ToolPattern string

	// Callback is the function invoked when the hook fires.
	Callback HookCallback
}

// HookCallback is the function signature for hook handlers.
type HookCallback func(ctx context.Context, event HookInput) (HookOutput, error)

// HookInput carries context about the event that triggered the hook.
type HookInput struct {
	// Event is the hook event type.
	Event HookEvent

	// SessionID is the current session ID.
	SessionID string

	// Message is the message that triggered the hook.
	// Set for all hook events.
	Message Message

	// ToolName is the name of the tool being invoked.
	// Set for PreToolUse and PostToolUse hooks.
	ToolName string

	// ToolInput is the raw JSON input to the tool.
	// Set for PreToolUse hooks.
	ToolInput json.RawMessage

	// ToolOutput is the output from the tool.
	// Set for PostToolUse hooks.
	ToolOutput string
}

// HookOutput controls what happens after the hook executes.
type HookOutput struct {
	// Block, if true, indicates the tool call should be blocked.
	// Only meaningful for PreToolUse hooks.
	Block bool

	// Reason provides an explanation for why the tool call was blocked.
	Reason string
}

// hookRunner manages hook execution and tracks tool_use_id to tool name
// mappings for PostToolUse pattern matching.
type hookRunner struct {
	hooks    []HookRegistration
	toolMap  map[string]string // tool_use_id -> tool name
}

// newHookRunner creates a new hookRunner with the given hooks.
func newHookRunner(hooks []HookRegistration) *hookRunner {
	return &hookRunner{
		hooks:   hooks,
		toolMap: make(map[string]string),
	}
}

// fireHooks invokes all matching hooks for the given event and message.
// Hooks are fired synchronously in registration order.
// Errors from hook callbacks are silently ignored to avoid breaking the stream.
func (hr *hookRunner) fireHooks(ctx context.Context, sessionID string, msg Message) {
	// Fire HookMessage for every message.
	for i := range hr.hooks {
		if hr.hooks[i].Event == HookMessage && hr.hooks[i].Callback != nil {
			input := HookInput{
				Event:     HookMessage,
				SessionID: sessionID,
				Message:   msg,
			}
			_, _ = hr.hooks[i].Callback(ctx, input)
		}
	}

	switch m := msg.(type) {
	case *AssistantMessage:
		// Track tool_use_id -> tool name mapping and fire HookPreToolUse.
		for _, block := range m.Content {
			tu, ok := block.(*ToolUseBlock)
			if !ok {
				continue
			}
			// Record the mapping for later PostToolUse lookup.
			hr.toolMap[tu.ID] = tu.Name

			for i := range hr.hooks {
				if hr.hooks[i].Event != HookPreToolUse || hr.hooks[i].Callback == nil {
					continue
				}
				if !matchToolPattern(hr.hooks[i].ToolPattern, tu.Name) {
					continue
				}
				input := HookInput{
					Event:     HookPreToolUse,
					SessionID: sessionID,
					Message:   msg,
					ToolName:  tu.Name,
					ToolInput: tu.Input,
				}
				_, _ = hr.hooks[i].Callback(ctx, input)
			}
		}

	case *UserMessage:
		// Fire HookPostToolUse for each ToolResultBlock.
		for _, block := range m.Content {
			tr, ok := block.(*ToolResultBlock)
			if !ok {
				continue
			}
			// Look up the tool name from the tracked mapping.
			toolName := hr.toolMap[tr.ToolUseID]

			for i := range hr.hooks {
				if hr.hooks[i].Event != HookPostToolUse || hr.hooks[i].Callback == nil {
					continue
				}
				if !matchToolPattern(hr.hooks[i].ToolPattern, toolName) {
					continue
				}
				input := HookInput{
					Event:      HookPostToolUse,
					SessionID:  sessionID,
					Message:    msg,
					ToolName:   toolName,
					ToolOutput: tr.Content,
				}
				_, _ = hr.hooks[i].Callback(ctx, input)
			}
		}

	case *ResultMessage:
		// Fire HookResult for ResultMessage.
		for i := range hr.hooks {
			if hr.hooks[i].Event == HookResult && hr.hooks[i].Callback != nil {
				input := HookInput{
					Event:     HookResult,
					SessionID: sessionID,
					Message:   msg,
				}
				_, _ = hr.hooks[i].Callback(ctx, input)
			}
		}
	}
}

// matchToolPattern checks if a tool name matches the given regex pattern.
// If the pattern is empty, it matches all tools.
// If the pattern is invalid, it does not match.
func matchToolPattern(pattern, toolName string) bool {
	if pattern == "" {
		return true
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		// Invalid pattern silently does not match.
		return false
	}
	return re.MatchString(toolName)
}
