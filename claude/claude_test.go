package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/albertocavalcante/claude-agent-sdk-go/internal/transport"
)

func TestQueryWithMockTransport(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init"}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"Four."}],"model":"claude-haiku-3.5","stop_reason":"end_turn"}`),
			json.RawMessage(`{"type":"result","is_error":false,"duration_ms":500.0,"cost_usd":0.001,"input_tokens":10,"output_tokens":5,"session_id":"sess_test"}`),
		},
	}

	ctx := context.Background()
	ch := queryWithTransport(ctx, "What is 2+2?", Options{Model: "haiku"}, mock)

	var messages []Message
	var errors []error

	for msg := range ch {
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

	// Check system message.
	sm, ok := messages[0].(*SystemMessage)
	if !ok {
		t.Fatalf("expected *SystemMessage, got %T", messages[0])
	}
	if sm.Subtype != "init" {
		t.Errorf("expected subtype 'init', got %q", sm.Subtype)
	}

	// Check assistant message.
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
	if tb.Text != "Four." {
		t.Errorf("expected text 'Four.', got %q", tb.Text)
	}

	// Check result message.
	rm, ok := messages[2].(*ResultMessage)
	if !ok {
		t.Fatalf("expected *ResultMessage, got %T", messages[2])
	}
	if rm.InputTokens != 10 {
		t.Errorf("expected input_tokens 10, got %d", rm.InputTokens)
	}
	if rm.OutputTokens != 5 {
		t.Errorf("expected output_tokens 5, got %d", rm.OutputTokens)
	}
}

func TestQueryWithMockTransportStartError(t *testing.T) {
	mock := &transport.MockTransport{
		StartErr: fmt.Errorf("connection refused"),
	}

	ctx := context.Background()
	ch := queryWithTransport(ctx, "test", Options{}, mock)

	var gotError bool
	for msg := range ch {
		if msg.Err != nil {
			gotError = true
		}
	}

	if !gotError {
		t.Fatal("expected an error from failed start")
	}
}

func TestQueryWithContextCancellation(t *testing.T) {
	// Create a mock that sends many messages.
	lines := make([]json.RawMessage, 100)
	for i := range lines {
		lines[i] = json.RawMessage(fmt.Sprintf(`{"type":"assistant","content":[{"type":"text","text":"msg %d"}]}`, i))
	}

	mock := &transport.MockTransport{RawLines: lines}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch := queryWithTransport(ctx, "test", Options{}, mock)

	// Read a few messages then cancel.
	count := 0
	for msg := range ch {
		if msg.Err != nil {
			break
		}
		count++
		if count >= 3 {
			cancel()
		}
	}

	// We should have gotten at least 3 messages but likely not all 100.
	if count < 3 {
		t.Errorf("expected at least 3 messages before cancellation, got %d", count)
	}
}

func TestQueryWithUnknownMessageType(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init"}`),
			json.RawMessage(`{"type":"new_future_type","data":"something"}`),
			json.RawMessage(`{"type":"result","is_error":false}`),
		},
	}

	ctx := context.Background()
	ch := queryWithTransport(ctx, "test", Options{}, mock)

	var messages []Message
	for msg := range ch {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
		messages = append(messages, msg.Message)
	}

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	um, ok := messages[1].(*UnknownMessage)
	if !ok {
		t.Fatalf("expected *UnknownMessage for unknown type, got %T", messages[1])
	}
	if um.Type() != "new_future_type" {
		t.Errorf("expected type 'new_future_type', got %q", um.Type())
	}
}

func TestQueryWithToolUseFlow(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"system","subtype":"init"}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_abc","name":"Bash","input":{"command":"echo hello"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_abc","content":"hello\n","is_error":false}]}`),
			json.RawMessage(`{"type":"assistant","content":[{"type":"text","text":"The command printed hello."}],"stop_reason":"end_turn"}`),
			json.RawMessage(`{"type":"result","is_error":false,"duration_ms":2000,"cost_usd":0.005,"input_tokens":50,"output_tokens":20,"session_id":"sess_tool","num_turns":2}`),
		},
	}

	ctx := context.Background()
	ch := queryWithTransport(ctx, "run echo hello", Options{}, mock)

	var messages []Message
	for msg := range ch {
		if msg.Err != nil {
			t.Fatalf("unexpected error: %v", msg.Err)
		}
		messages = append(messages, msg.Message)
	}

	if len(messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(messages))
	}

	// Verify tool use in first assistant message.
	am1 := messages[1].(*AssistantMessage)
	if len(am1.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(am1.Content))
	}
	tu, ok := am1.Content[0].(*ToolUseBlock)
	if !ok {
		t.Fatalf("expected *ToolUseBlock, got %T", am1.Content[0])
	}
	if tu.Name != "Bash" {
		t.Errorf("expected tool name 'Bash', got %q", tu.Name)
	}

	// Verify tool result in user message.
	um := messages[2].(*UserMessage)
	if len(um.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(um.Content))
	}
	tr, ok := um.Content[0].(*ToolResultBlock)
	if !ok {
		t.Fatalf("expected *ToolResultBlock, got %T", um.Content[0])
	}
	if tr.ToolUseID != "tu_abc" {
		t.Errorf("expected tool_use_id 'tu_abc', got %q", tr.ToolUseID)
	}

	// Verify result message.
	rm := messages[4].(*ResultMessage)
	if rm.NumTurns != 2 {
		t.Errorf("expected num_turns 2, got %d", rm.NumTurns)
	}
}
