package claude

import (
	"context"
	"encoding/json"
	"fmt"
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

// compiledHook is an internal representation of a HookRegistration with
// a pre-compiled regex pattern for efficient matching.
type compiledHook struct {
	event    HookEvent
	pattern  *regexp.Regexp // nil means match all
	callback HookCallback
}

// ValidateHooks checks that all hook registrations have valid tool patterns.
// Returns an error if any pattern fails to compile as a regex.
func ValidateHooks(hooks []HookRegistration) error {
	for i, h := range hooks {
		if h.ToolPattern != "" {
			if _, err := regexp.Compile(h.ToolPattern); err != nil {
				return fmt.Errorf("hook %d: invalid tool pattern %q: %w", i, h.ToolPattern, err)
			}
		}
	}
	return nil
}

// hookRunner manages hook execution and tracks tool_use_id to tool name
// mappings for PostToolUse pattern matching.
type hookRunner struct {
	hooks   []compiledHook
	toolMap map[string]string // tool_use_id -> tool name
}

// newHookRunner creates a new hookRunner with the given hooks.
// Patterns are compiled once here for efficient matching.
func newHookRunner(hooks []HookRegistration) *hookRunner {
	compiled := make([]compiledHook, 0, len(hooks))
	for _, h := range hooks {
		ch := compiledHook{
			event:    h.Event,
			callback: h.Callback,
		}
		if h.ToolPattern != "" {
			// Pattern was already validated by ValidateHooks or NewClient.
			// Best-effort compile; skip hook if pattern is somehow invalid.
			re, err := regexp.Compile(h.ToolPattern)
			if err == nil {
				ch.pattern = re
			}
		}
		compiled = append(compiled, ch)
	}
	return &hookRunner{
		hooks:   compiled,
		toolMap: make(map[string]string),
	}
}

// fireHooks invokes all matching hooks for the given event and message.
// Hooks are fired synchronously in registration order.
// Errors from hook callbacks are silently ignored to avoid breaking the stream.
func (hr *hookRunner) fireHooks(ctx context.Context, sessionID string, msg Message) {
	// Fire HookMessage for every message.
	for i := range hr.hooks {
		if hr.hooks[i].event == HookMessage && hr.hooks[i].callback != nil {
			input := HookInput{
				Event:     HookMessage,
				SessionID: sessionID,
				Message:   msg,
			}
			_, _ = hr.hooks[i].callback(ctx, input)
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
				if hr.hooks[i].event != HookPreToolUse || hr.hooks[i].callback == nil {
					continue
				}
				if !hr.matchToolPattern(i, tu.Name) {
					continue
				}
				input := HookInput{
					Event:     HookPreToolUse,
					SessionID: sessionID,
					Message:   msg,
					ToolName:  tu.Name,
					ToolInput: tu.Input,
				}
				_, _ = hr.hooks[i].callback(ctx, input)
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
				if hr.hooks[i].event != HookPostToolUse || hr.hooks[i].callback == nil {
					continue
				}
				if !hr.matchToolPattern(i, toolName) {
					continue
				}
				input := HookInput{
					Event:      HookPostToolUse,
					SessionID:  sessionID,
					Message:    msg,
					ToolName:   toolName,
					ToolOutput: tr.Content,
				}
				_, _ = hr.hooks[i].callback(ctx, input)
			}
		}

	case *ResultMessage:
		// Fire HookResult for ResultMessage.
		for i := range hr.hooks {
			if hr.hooks[i].event == HookResult && hr.hooks[i].callback != nil {
				input := HookInput{
					Event:     HookResult,
					SessionID: sessionID,
					Message:   msg,
				}
				_, _ = hr.hooks[i].callback(ctx, input)
			}
		}
	}
}

// matchToolPattern checks if a tool name matches the compiled pattern at index i.
// A nil pattern matches all tools.
func (hr *hookRunner) matchToolPattern(i int, toolName string) bool {
	if hr.hooks[i].pattern == nil {
		return true
	}
	return hr.hooks[i].pattern.MatchString(toolName)
}
