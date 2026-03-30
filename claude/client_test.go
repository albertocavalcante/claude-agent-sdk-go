package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/internal/transport"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name          string
		opts          Options
		wantSessionID string
		wantModel     string
	}{
		{
			name:          "default options",
			opts:          Options{},
			wantSessionID: "",
			wantModel:     "",
		},
		{
			name:          "with model",
			opts:          Options{Model: "haiku"},
			wantSessionID: "",
			wantModel:     "haiku",
		},
		{
			name:          "with session ID",
			opts:          Options{SessionID: "existing-session"},
			wantSessionID: "existing-session",
		},
		{
			name: "with hooks",
			opts: Options{
				Hooks: []HookRegistration{
					{Event: HookMessage},
				},
			},
			wantSessionID: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewClient(tt.opts)
			if client == nil {
				t.Fatal("NewClient returned nil")
			}
			if got := client.SessionID(); got != tt.wantSessionID {
				t.Errorf("SessionID() = %q, want %q", got, tt.wantSessionID)
			}
		})
	}
}

func TestClientQuery(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init"}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Hello!"}],"model":"claude-haiku","stop_reason":"end_turn"}`),
			json.RawMessage(`{"type":"result","is_error":false,"duration_ms":100,"cost_usd":0.001,"input_tokens":10,"output_tokens":5,"session_id":"sess_from_cli"}`),
		},
	}

	client := newClientWithTransport(Options{Model: "haiku"}, mock)
	ctx := context.Background()

	var messages []Message
	var errors []error

	for msg := range client.Query(ctx, "Hello") {
		if msg.Err != nil {
			errors = append(errors, msg.Err)
		} else {
			messages = append(messages, msg.Message)
		}
	}

	if len(errors) > 0 {
		t.Fatalf("unexpected errors: %v", errors)
	}

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	// Verify system message.
	if _, ok := messages[0].(*SystemMessage); !ok {
		t.Errorf("expected *SystemMessage, got %T", messages[0])
	}

	// Verify assistant message.
	am, ok := messages[1].(*AssistantMessage)
	if !ok {
		t.Fatalf("expected *AssistantMessage, got %T", messages[1])
	}
	if len(am.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(am.Content))
	}
	tb, ok := am.Content[0].(*TextBlock)
	if !ok {
		t.Fatalf("expected *TextBlock, got %T", am.Content[0])
	}
	if tb.Text != "Hello!" {
		t.Errorf("expected text 'Hello!', got %q", tb.Text)
	}

	// Verify result message.
	rm, ok := messages[2].(*ResultMessage)
	if !ok {
		t.Fatalf("expected *ResultMessage, got %T", messages[2])
	}
	if rm.SessionID != "sess_from_cli" {
		t.Errorf("expected session_id 'sess_from_cli', got %q", rm.SessionID)
	}
}

func TestClientMultiTurn(t *testing.T) {
	// First turn mock.
	mock1 := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"First response"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_multi"}`),
		},
	}

	client := newClientWithTransport(Options{}, mock1)
	ctx := context.Background()

	// First query.
	for msg := range client.Query(ctx, "First prompt") {
		if msg.Err != nil {
			t.Fatalf("unexpected error in first query: %v", msg.Err)
		}
	}

	// After first query, session ID should be captured from result.
	sid := client.SessionID()
	if sid != "sess_multi" {
		t.Fatalf("expected session ID 'sess_multi' after first query, got %q", sid)
	}

	// Second turn mock. We need to inject a new transport for the second call.
	// Since the client uses the injected transport, we update RawLines.
	mock2 := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Second response"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_multi"}`),
		},
	}

	// Replace transport for the second call.
	client.transport = mock2

	// Second query should use the same session ID.
	for msg := range client.Query(ctx, "Second prompt") {
		if msg.Err != nil {
			t.Fatalf("unexpected error in second query: %v", msg.Err)
		}
	}

	// Session ID should remain the same.
	if got := client.SessionID(); got != "sess_multi" {
		t.Errorf("expected session ID 'sess_multi' after second query, got %q", got)
	}
}

func TestClientHookMessage(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init"}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Hi"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_hook"}`),
		},
	}

	var mu sync.Mutex
	var hookMessages []Message

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookMessage,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					hookMessages = append(hookMessages, event.Message)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// HookMessage should fire for every message: system, assistant, result.
	if len(hookMessages) != 3 {
		t.Fatalf("expected 3 hook messages, got %d", len(hookMessages))
	}

	expectedTypes := []string{"system", "assistant", "result"}
	for i, expected := range expectedTypes {
		if hookMessages[i].Type() != expected {
			t.Errorf("hook message %d: expected type %q, got %q", i, expected, hookMessages[i].Type())
		}
	}
}

func TestClientHookPreToolUse(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"file1.txt\nfile2.txt"}]}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Here are the files."}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_tool"}`),
		},
	}

	var mu sync.Mutex
	var toolNames []string
	var toolInputs []json.RawMessage

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					toolNames = append(toolNames, event.ToolName)
					toolInputs = append(toolInputs, event.ToolInput)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "list files") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if len(toolNames) != 1 {
		t.Fatalf("expected 1 PreToolUse hook call, got %d", len(toolNames))
	}
	if toolNames[0] != "Bash" {
		t.Errorf("expected tool name 'Bash', got %q", toolNames[0])
	}
	if string(toolInputs[0]) != `{"command":"ls"}` {
		t.Errorf("expected tool input '{\"command\":\"ls\"}', got %q", string(toolInputs[0]))
	}
}

func TestClientHookPostToolUse(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_2","name":"ReadFile","input":{"path":"/tmp/test"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_2","content":"file contents here"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_post"}`),
		},
	}

	var mu sync.Mutex
	var postToolNames []string
	var postToolOutputs []string

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookPostToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					postToolNames = append(postToolNames, event.ToolName)
					postToolOutputs = append(postToolOutputs, event.ToolOutput)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "read file") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if len(postToolNames) != 1 {
		t.Fatalf("expected 1 PostToolUse hook call, got %d", len(postToolNames))
	}
	if postToolNames[0] != "ReadFile" {
		t.Errorf("expected tool name 'ReadFile', got %q", postToolNames[0])
	}
	if postToolOutputs[0] != "file contents here" {
		t.Errorf("expected tool output 'file contents here', got %q", postToolOutputs[0])
	}
}

func TestClientHookToolPattern(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_a","name":"Bash","input":{"command":"ls"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_a","content":"output1"}]}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_b","name":"ReadFile","input":{"path":"/tmp"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_b","content":"output2"}]}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_c","name":"WriteFile","input":{"path":"/tmp"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_c","content":"output3"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_pat"}`),
		},
	}

	var mu sync.Mutex
	var matchedToolNames []string

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event:       HookPreToolUse,
				ToolPattern: "^(Read|Write)File$",
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					matchedToolNames = append(matchedToolNames, event.ToolName)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "do stuff") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// Only ReadFile and WriteFile should match, not Bash.
	if len(matchedToolNames) != 2 {
		t.Fatalf("expected 2 matched tools, got %d: %v", len(matchedToolNames), matchedToolNames)
	}
	if matchedToolNames[0] != "ReadFile" {
		t.Errorf("expected first match 'ReadFile', got %q", matchedToolNames[0])
	}
	if matchedToolNames[1] != "WriteFile" {
		t.Errorf("expected second match 'WriteFile', got %q", matchedToolNames[1])
	}
}

func TestClientHookToolPatternPostToolUse(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_x","name":"Bash","input":{"command":"ls"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_x","content":"bash output"}]}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_y","name":"ReadFile","input":{"path":"/tmp"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_y","content":"file output"}]}`),
			json.RawMessage(`{"type":"result","is_error":false}`),
		},
	}

	var mu sync.Mutex
	var matchedOutputs []string

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event:       HookPostToolUse,
				ToolPattern: "Bash",
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					matchedOutputs = append(matchedOutputs, event.ToolOutput)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// Only the Bash tool result should match.
	if len(matchedOutputs) != 1 {
		t.Fatalf("expected 1 matched PostToolUse, got %d", len(matchedOutputs))
	}
	if matchedOutputs[0] != "bash output" {
		t.Errorf("expected 'bash output', got %q", matchedOutputs[0])
	}
}

func TestClientHookResult(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Done"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"cost_usd":0.005,"input_tokens":100,"output_tokens":50,"session_id":"sess_res"}`),
		},
	}

	var mu sync.Mutex
	var resultMsg *ResultMessage

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookResult,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					if rm, ok := event.Message.(*ResultMessage); ok {
						resultMsg = rm
					}
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if resultMsg == nil {
		t.Fatal("HookResult callback was not called")
	}
	if resultMsg.Cost != 0.005 {
		t.Errorf("expected cost 0.005, got %f", resultMsg.Cost)
	}
	if resultMsg.InputTokens != 100 {
		t.Errorf("expected input_tokens 100, got %d", resultMsg.InputTokens)
	}
	if resultMsg.OutputTokens != 50 {
		t.Errorf("expected output_tokens 50, got %d", resultMsg.OutputTokens)
	}
}

func TestClientSessionIDFromResult(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Hi"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_captured"}`),
		},
	}

	client := newClientWithTransport(Options{}, mock)
	ctx := context.Background()

	// Before query, session ID should be empty.
	if sid := client.SessionID(); sid != "" {
		t.Fatalf("expected empty session ID before query, got %q", sid)
	}

	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	// After query, session ID should be captured from the ResultMessage.
	if sid := client.SessionID(); sid != "sess_captured" {
		t.Errorf("expected session ID 'sess_captured', got %q", sid)
	}
}

func TestClientSessionIDPreservedWithExistingID(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_new"}`),
		},
	}

	client := newClientWithTransport(Options{SessionID: "sess_existing"}, mock)
	ctx := context.Background()

	// Before query, should have the provided session ID.
	if sid := client.SessionID(); sid != "sess_existing" {
		t.Fatalf("expected session ID 'sess_existing', got %q", sid)
	}

	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	// After query, session ID should be updated from result.
	if sid := client.SessionID(); sid != "sess_new" {
		t.Errorf("expected session ID 'sess_new' after query, got %q", sid)
	}
}

func TestClientClose(t *testing.T) {
	client := NewClient(Options{})

	if err := client.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	// Query after close should return an error.
	ctx := context.Background()
	var gotError bool
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			gotError = true
		}
	}
	if !gotError {
		t.Error("expected error from query on closed client")
	}
}

func TestClientGeneratesSessionID(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"result","is_error":false}`),
		},
	}

	client := newClientWithTransport(Options{}, mock)
	ctx := context.Background()

	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	// Session ID should have been auto-generated (non-empty).
	sid := client.SessionID()
	if sid == "" {
		t.Error("expected non-empty session ID after query")
	}
	// Should look like a UUID (36 chars with dashes).
	if len(sid) < 30 {
		t.Errorf("expected UUID-like session ID, got %q", sid)
	}
}

func TestClientHookErrorDoesNotBreakStream(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Hello"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_err"}`),
		},
	}

	var mu sync.Mutex
	var resultHookCalled bool

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookMessage,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					// Return an error from the hook.
					return HookOutput{}, fmt.Errorf("hook error")
				},
			},
			{
				Event: HookResult,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					resultHookCalled = true
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	var messages []Message
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
		messages = append(messages, msg.Message)
	}

	// Messages should still be received despite hook errors.
	if len(messages) != 2 {
		t.Fatalf("expected 2 messages despite hook error, got %d", len(messages))
	}

	mu.Lock()
	defer mu.Unlock()
	if !resultHookCalled {
		t.Error("expected result hook to be called despite earlier hook error")
	}
}

func TestClientHookSessionIDInEvent(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Hi"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_hook_id"}`),
		},
	}

	var mu sync.Mutex
	var hookSessionIDs []string

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookMessage,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					hookSessionIDs = append(hookSessionIDs, event.SessionID)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	if len(hookSessionIDs) != 2 {
		t.Fatalf("expected 2 session IDs from hooks, got %d", len(hookSessionIDs))
	}

	// All session IDs should be non-empty.
	for i, sid := range hookSessionIDs {
		if sid == "" {
			t.Errorf("hook %d: expected non-empty session ID", i)
		}
	}
}

func TestClientMultipleHooksSameEvent(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_m","name":"Bash","input":{}}]}`),
			json.RawMessage(`{"type":"result","is_error":false}`),
		},
	}

	var mu sync.Mutex
	var order []int

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					order = append(order, 1)
					return HookOutput{}, nil
				},
			},
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					order = append(order, 2)
					return HookOutput{}, nil
				},
			},
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					defer mu.Unlock()
					order = append(order, 3)
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	// Hooks should fire in registration order.
	if len(order) != 3 {
		t.Fatalf("expected 3 hook calls, got %d", len(order))
	}
	for i, v := range order {
		if v != i+1 {
			t.Errorf("hook %d: expected order %d, got %d", i, i+1, v)
		}
	}
}

func TestClientNoHooks(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"No hooks"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_no_hooks"}`),
		},
	}

	// Client with no hooks should work fine.
	client := newClientWithTransport(Options{}, mock)
	ctx := context.Background()

	var messages []Message
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
		messages = append(messages, msg.Message)
	}

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}
}

func TestClientQueryStartError(t *testing.T) {
	mock := &transport.MockTransport{
		StartErr: fmt.Errorf("failed to start"),
	}

	client := newClientWithTransport(Options{}, mock)
	ctx := context.Background()

	var gotError bool
	for msg := range client.Query(ctx, "test") {
		if msg.Err != nil {
			gotError = true
		}
	}

	if !gotError {
		t.Error("expected error from failed transport start")
	}
}

func TestHookRunnerMatchToolPattern(t *testing.T) {
	tests := []struct {
		name     string
		pattern  string
		toolName string
		want     bool
	}{
		{
			name:     "empty pattern matches all",
			pattern:  "",
			toolName: "Bash",
			want:     true,
		},
		{
			name:     "exact match",
			pattern:  "^Bash$",
			toolName: "Bash",
			want:     true,
		},
		{
			name:     "no match",
			pattern:  "^Bash$",
			toolName: "ReadFile",
			want:     false,
		},
		{
			name:     "partial match",
			pattern:  "File",
			toolName: "ReadFile",
			want:     true,
		},
		{
			name:     "regex alternation",
			pattern:  "^(Bash|ReadFile)$",
			toolName: "ReadFile",
			want:     true,
		},
		{
			name:     "regex alternation no match",
			pattern:  "^(Bash|ReadFile)$",
			toolName: "WriteFile",
			want:     false,
		},
		{
			name:     "invalid regex falls back to match all",
			pattern:  "[invalid",
			toolName: "Bash",
			want:     true, // invalid patterns are caught by ValidateHooks; hookRunner treats compile failure as match-all
		},
		{
			name:     "wildcard match",
			pattern:  ".*File.*",
			toolName: "ReadFile",
			want:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runner := newHookRunner([]HookRegistration{
				{Event: HookPreToolUse, ToolPattern: tt.pattern},
			})
			got := runner.matchToolPattern(0, tt.toolName)
			if got != tt.want {
				t.Errorf("matchToolPattern(%q, %q) = %v, want %v", tt.pattern, tt.toolName, got, tt.want)
			}
		})
	}
}

func TestClientCancelAndReplace(t *testing.T) {
	// Verifies that a new Query cancels the previous in-flight query,
	// preventing "session already in use" errors from the CLI.

	// First query: slow mock that blocks until context is cancelled.
	slowMock := &transport.MockTransport{
		SlowMode: true, // Start succeeds but Lines() blocks until Close
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_slow"}`),
		},
	}

	client := newClientWithTransport(Options{}, slowMock)
	ctx := context.Background()

	// Start first query (will block on slow mock).
	q1Ch := client.Query(ctx, "first")

	// Wait for the goroutine to register as active.
	time.Sleep(50 * time.Millisecond)

	// Swap transport for the second query. The first query already holds
	// a reference to slowMock, so this only affects new queries.
	fastMock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"fast response"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_fast"}`),
		},
	}
	client.mu.Lock()
	client.transport = fastMock
	client.mu.Unlock()

	var q2Messages []Message
	for msg := range client.Query(ctx, "second") {
		if msg.Err != nil {
			t.Fatalf("second query error: %v", msg.Err)
		}
		q2Messages = append(q2Messages, msg.Message)
	}

	// Second query should complete successfully.
	if len(q2Messages) != 2 {
		t.Fatalf("expected 2 messages from second query, got %d", len(q2Messages))
	}

	// First query's channel should be closed (cancelled).
	for range q1Ch {
		// drain remaining
	}

	// Session ID should be from the second query.
	if sid := client.SessionID(); sid != "sess_fast" {
		t.Errorf("expected session ID 'sess_fast', got %q", sid)
	}
}

func TestGenerateSessionID(t *testing.T) {
	id1 := generateSessionID()
	id2 := generateSessionID()

	if id1 == "" {
		t.Error("generateSessionID returned empty string")
	}
	if id2 == "" {
		t.Error("generateSessionID returned empty string")
	}
	if id1 == id2 {
		t.Error("generateSessionID returned same ID twice")
	}

	// Should contain dashes like a UUID.
	dashCount := 0
	for _, c := range id1 {
		if c == '-' {
			dashCount++
		}
	}
	if dashCount != 4 {
		t.Errorf("expected 4 dashes in UUID, got %d in %q", dashCount, id1)
	}
}
