package claude

import "testing"

func TestMessageTypeInterface(t *testing.T) {
	// Verify all message types implement the Message interface.
	var _ Message = &AssistantMessage{}
	var _ Message = &UserMessage{}
	var _ Message = &ResultMessage{}
	var _ Message = &SystemMessage{}
	var _ Message = &UnknownMessage{}

	tests := []struct {
		name     string
		msg      Message
		expected string
	}{
		{"AssistantMessage", &AssistantMessage{}, "assistant"},
		{"UserMessage", &UserMessage{}, "user"},
		{"ResultMessage", &ResultMessage{}, "result"},
		{"SystemMessage", &SystemMessage{}, "system"},
		{"UnknownMessage", &UnknownMessage{RawType: "custom"}, "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.Type(); got != tt.expected {
				t.Errorf("Type() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestContentBlockTypeInterface(t *testing.T) {
	// Verify all content block types implement the ContentBlock interface.
	var _ ContentBlock = &TextBlock{}
	var _ ContentBlock = &ToolUseBlock{}
	var _ ContentBlock = &ToolResultBlock{}
	var _ ContentBlock = &ThinkingBlock{}

	tests := []struct {
		name     string
		block    ContentBlock
		expected string
	}{
		{"TextBlock", &TextBlock{Text: "hello"}, "text"},
		{"ToolUseBlock", &ToolUseBlock{ID: "1", Name: "test"}, "tool_use"},
		{"ToolResultBlock", &ToolResultBlock{ToolUseID: "1"}, "tool_result"},
		{"ThinkingBlock", &ThinkingBlock{Thinking: "hmm"}, "thinking"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.block.BlockType(); got != tt.expected {
				t.Errorf("BlockType() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestErrorTypes(t *testing.T) {
	// Verify all error types implement the error interface.
	var _ error = &CLIError{}
	var _ error = &ProtocolError{}
	var _ error = &ProcessError{}

	t.Run("CLIError", func(t *testing.T) {
		e := &CLIError{Message: "fail", Stderr: "details"}
		if e.Error() == "" {
			t.Error("expected non-empty error string")
		}
	})

	t.Run("CLIError without stderr", func(t *testing.T) {
		e := &CLIError{Message: "fail"}
		got := e.Error()
		if got != "claude cli error: fail" {
			t.Errorf("unexpected error string: %q", got)
		}
	})

	t.Run("ProtocolError", func(t *testing.T) {
		e := &ProtocolError{Message: "bad json", Raw: []byte("{")}
		if e.Error() == "" {
			t.Error("expected non-empty error string")
		}
	})

	t.Run("ProtocolError without raw", func(t *testing.T) {
		e := &ProtocolError{Message: "bad json"}
		got := e.Error()
		if got != "protocol error: bad json" {
			t.Errorf("unexpected error string: %q", got)
		}
	})

	t.Run("ProcessError", func(t *testing.T) {
		e := &ProcessError{Message: "exited", ExitCode: 1}
		if e.Error() == "" {
			t.Error("expected non-empty error string")
		}
	})
}
