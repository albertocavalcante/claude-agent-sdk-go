package claude

import (
	"testing"
)

func TestParseAssistantMessage(t *testing.T) {
	data := []byte(`{
		"type": "assistant",
		"model": "claude-sonnet-4-20250514",
		"stop_reason": "end_turn",
		"content": [
			{"type": "text", "text": "Hello, world!"},
			{"type": "thinking", "thinking": "Let me think about this..."}
		]
	}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	am, ok := msg.(*AssistantMessage)
	if !ok {
		t.Fatalf("expected *AssistantMessage, got %T", msg)
	}

	if am.Type() != "assistant" {
		t.Errorf("expected type 'assistant', got %q", am.Type())
	}
	if am.Model != "claude-sonnet-4-20250514" {
		t.Errorf("expected model 'claude-sonnet-4-20250514', got %q", am.Model)
	}
	if am.StopReason != "end_turn" {
		t.Errorf("expected stop_reason 'end_turn', got %q", am.StopReason)
	}
	if len(am.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(am.Content))
	}

	tb, ok := am.Content[0].(*TextBlock)
	if !ok {
		t.Fatalf("expected *TextBlock, got %T", am.Content[0])
	}
	if tb.Text != "Hello, world!" {
		t.Errorf("expected text 'Hello, world!', got %q", tb.Text)
	}
	if tb.BlockType() != "text" {
		t.Errorf("expected block type 'text', got %q", tb.BlockType())
	}

	thb, ok := am.Content[1].(*ThinkingBlock)
	if !ok {
		t.Fatalf("expected *ThinkingBlock, got %T", am.Content[1])
	}
	if thb.Thinking != "Let me think about this..." {
		t.Errorf("unexpected thinking content: %q", thb.Thinking)
	}
	if thb.BlockType() != "thinking" {
		t.Errorf("expected block type 'thinking', got %q", thb.BlockType())
	}
}

func TestParseAssistantMessageWithToolUse(t *testing.T) {
	data := []byte(`{
		"type": "assistant",
		"content": [
			{"type": "tool_use", "id": "tu_123", "name": "read_file", "input": {"path": "/tmp/test.txt"}}
		]
	}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	am := msg.(*AssistantMessage)
	if len(am.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(am.Content))
	}

	tu, ok := am.Content[0].(*ToolUseBlock)
	if !ok {
		t.Fatalf("expected *ToolUseBlock, got %T", am.Content[0])
	}
	if tu.ID != "tu_123" {
		t.Errorf("expected ID 'tu_123', got %q", tu.ID)
	}
	if tu.Name != "read_file" {
		t.Errorf("expected name 'read_file', got %q", tu.Name)
	}
	if tu.BlockType() != "tool_use" {
		t.Errorf("expected block type 'tool_use', got %q", tu.BlockType())
	}
}

func TestParseResultMessage(t *testing.T) {
	data := []byte(`{
		"type": "result",
		"is_error": false,
		"duration_ms": 1234.5,
		"cost_usd": 0.0042,
		"input_tokens": 100,
		"output_tokens": 50,
		"session_id": "sess_abc123",
		"num_turns": 1
	}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rm, ok := msg.(*ResultMessage)
	if !ok {
		t.Fatalf("expected *ResultMessage, got %T", msg)
	}

	if rm.Type() != "result" {
		t.Errorf("expected type 'result', got %q", rm.Type())
	}
	if rm.IsError {
		t.Error("expected IsError to be false")
	}
	if rm.Duration != 1234.5 {
		t.Errorf("expected duration 1234.5, got %f", rm.Duration)
	}
	if rm.Cost != 0.0042 {
		t.Errorf("expected cost 0.0042, got %f", rm.Cost)
	}
	if rm.InputTokens != 100 {
		t.Errorf("expected input_tokens 100, got %d", rm.InputTokens)
	}
	if rm.OutputTokens != 50 {
		t.Errorf("expected output_tokens 50, got %d", rm.OutputTokens)
	}
	if rm.SessionID != "sess_abc123" {
		t.Errorf("expected session_id 'sess_abc123', got %q", rm.SessionID)
	}
}

func TestParseSystemMessage(t *testing.T) {
	data := []byte(`{"type": "system", "subtype": "init"}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sm, ok := msg.(*SystemMessage)
	if !ok {
		t.Fatalf("expected *SystemMessage, got %T", msg)
	}

	if sm.Type() != "system" {
		t.Errorf("expected type 'system', got %q", sm.Type())
	}
	if sm.Subtype != "init" {
		t.Errorf("expected subtype 'init', got %q", sm.Subtype)
	}
}

func TestParseUserMessage(t *testing.T) {
	data := []byte(`{
		"type": "user",
		"content": [
			{"type": "tool_result", "tool_use_id": "tu_123", "content": "file contents here", "is_error": false}
		]
	}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	um, ok := msg.(*UserMessage)
	if !ok {
		t.Fatalf("expected *UserMessage, got %T", msg)
	}

	if um.Type() != "user" {
		t.Errorf("expected type 'user', got %q", um.Type())
	}
	if len(um.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(um.Content))
	}

	tr, ok := um.Content[0].(*ToolResultBlock)
	if !ok {
		t.Fatalf("expected *ToolResultBlock, got %T", um.Content[0])
	}
	if tr.ToolUseID != "tu_123" {
		t.Errorf("expected tool_use_id 'tu_123', got %q", tr.ToolUseID)
	}
	if tr.BlockType() != "tool_result" {
		t.Errorf("expected block type 'tool_result', got %q", tr.BlockType())
	}
}

func TestParseUnknownMessageType(t *testing.T) {
	data := []byte(`{"type": "future_type", "foo": "bar"}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unknown message types should NOT produce errors, got: %v", err)
	}

	um, ok := msg.(*UnknownMessage)
	if !ok {
		t.Fatalf("expected *UnknownMessage, got %T", msg)
	}

	if um.Type() != "future_type" {
		t.Errorf("expected type 'future_type', got %q", um.Type())
	}
	if um.Raw == nil {
		t.Error("expected Raw to be non-nil")
	}
}

func TestParseUnknownContentBlockType(t *testing.T) {
	data := []byte(`{
		"type": "assistant",
		"content": [
			{"type": "text", "text": "Hello"},
			{"type": "future_block_type", "data": "something"},
			{"type": "text", "text": "World"}
		]
	}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	am := msg.(*AssistantMessage)
	// Unknown block types are silently skipped, so we should have 2 text blocks.
	if len(am.Content) != 2 {
		t.Fatalf("expected 2 content blocks (unknown skipped), got %d", len(am.Content))
	}

	tb0 := am.Content[0].(*TextBlock)
	if tb0.Text != "Hello" {
		t.Errorf("expected 'Hello', got %q", tb0.Text)
	}
	tb1 := am.Content[1].(*TextBlock)
	if tb1.Text != "World" {
		t.Errorf("expected 'World', got %q", tb1.Text)
	}
}

func TestParseInvalidJSON(t *testing.T) {
	data := []byte(`not valid json`)

	_, err := ParseMessage(data)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}

	pe, ok := err.(*ProtocolError)
	if !ok {
		t.Fatalf("expected *ProtocolError, got %T", err)
	}
	if pe.Raw == nil {
		t.Error("expected Raw to be non-nil in ProtocolError")
	}
}

func TestParseEmptyContent(t *testing.T) {
	data := []byte(`{"type": "assistant", "content": []}`)

	msg, err := ParseMessage(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	am := msg.(*AssistantMessage)
	if len(am.Content) != 0 {
		t.Errorf("expected 0 content blocks, got %d", len(am.Content))
	}
}
