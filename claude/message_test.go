package claude

import (
	"fmt"
	"testing"
)

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

func TestErrorHelpers(t *testing.T) {
	t.Run("IsProcessError", func(t *testing.T) {
		err := &ProcessError{Message: "exit", ExitCode: 1}
		if !IsProcessError(err) {
			t.Error("expected IsProcessError to return true")
		}
		if IsProcessError(fmt.Errorf("not a process error")) {
			t.Error("expected IsProcessError to return false for non-ProcessError")
		}
	})

	t.Run("IsCLIError", func(t *testing.T) {
		err := &CLIError{Message: "fail"}
		if !IsCLIError(err) {
			t.Error("expected IsCLIError to return true")
		}
	})

	t.Run("IsProtocolError", func(t *testing.T) {
		err := &ProtocolError{Message: "bad json"}
		if !IsProtocolError(err) {
			t.Error("expected IsProtocolError to return true")
		}
	})

	t.Run("ExitCode", func(t *testing.T) {
		err := &ProcessError{Message: "exit", ExitCode: 42}
		if got := ExitCode(err); got != 42 {
			t.Errorf("ExitCode() = %d, want 42", got)
		}
		if got := ExitCode(fmt.Errorf("other")); got != -1 {
			t.Errorf("ExitCode(other) = %d, want -1", got)
		}
	})
}

func TestModelConstants(t *testing.T) {
	if ModelOpus == "" {
		t.Error("ModelOpus should not be empty")
	}
	if ModelSonnet == "" {
		t.Error("ModelSonnet should not be empty")
	}
	if ModelHaiku == "" {
		t.Error("ModelHaiku should not be empty")
	}
}

func TestPermissionModeConstants(t *testing.T) {
	if PermissionDefault != "default" {
		t.Errorf("PermissionDefault = %q, want 'default'", PermissionDefault)
	}
	if PermissionAcceptEdits != "acceptEdits" {
		t.Errorf("PermissionAcceptEdits = %q, want 'acceptEdits'", PermissionAcceptEdits)
	}
	if PermissionBypassPermissions != "bypassPermissions" {
		t.Errorf("PermissionBypassPermissions = %q, want 'bypassPermissions'", PermissionBypassPermissions)
	}
}

func TestValidateHooks(t *testing.T) {
	t.Run("valid hooks", func(t *testing.T) {
		hooks := []HookRegistration{
			{Event: HookPreToolUse, ToolPattern: "^Bash$"},
			{Event: HookPostToolUse, ToolPattern: ""},
		}
		if err := ValidateHooks(hooks); err != nil {
			t.Errorf("expected no error, got: %v", err)
		}
	})

	t.Run("invalid pattern", func(t *testing.T) {
		hooks := []HookRegistration{
			{Event: HookPreToolUse, ToolPattern: "[invalid"},
		}
		if err := ValidateHooks(hooks); err == nil {
			t.Error("expected error for invalid regex pattern")
		}
	})
}
