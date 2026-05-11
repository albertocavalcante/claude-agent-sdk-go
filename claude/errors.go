package claude

import (
	"errors"
	"fmt"
)

// CLIError indicates the Claude CLI returned an error message.
type CLIError struct {
	Message string
	Stderr  string
}

func (e *CLIError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("claude cli error: %s (stderr: %s)", e.Message, e.Stderr)
	}
	return fmt.Sprintf("claude cli error: %s", e.Message)
}

// ProtocolError indicates a failure to parse the CLI's JSON output.
type ProtocolError struct {
	Message string
	Raw     []byte
}

func (e *ProtocolError) Error() string {
	if len(e.Raw) > 0 {
		return fmt.Sprintf("protocol error: %s (raw: %s)", e.Message, string(e.Raw))
	}
	return fmt.Sprintf("protocol error: %s", e.Message)
}

// ProcessError indicates the CLI subprocess exited abnormally.
type ProcessError struct {
	Message  string
	ExitCode int
}

func (e *ProcessError) Error() string {
	return fmt.Sprintf("process error (exit %d): %s", e.ExitCode, e.Message)
}

// IsProcessError checks if the error is a ProcessError.
func IsProcessError(err error) bool {
	var pe *ProcessError
	return errors.As(err, &pe)
}

// IsCLIError checks if the error is a CLIError.
func IsCLIError(err error) bool {
	var ce *CLIError
	return errors.As(err, &ce)
}

// IsProtocolError checks if the error is a ProtocolError.
func IsProtocolError(err error) bool {
	var pe *ProtocolError
	return errors.As(err, &pe)
}

// ExitCode extracts the exit code from a ProcessError in the error chain.
// Returns -1 if the error is not a ProcessError.
func ExitCode(err error) int {
	var pe *ProcessError
	if errors.As(err, &pe) {
		return pe.ExitCode
	}
	return -1
}

// HookError wraps an error returned by a user-supplied hook callback.
// It carries enough context to identify which hook fired and on which tool,
// so callers can route or filter hook failures without parsing strings.
type HookError struct {
	Event    HookEvent
	ToolName string // empty for non-tool events (Message, Result)
	Err      error
}

func (e *HookError) Error() string {
	if e.ToolName != "" {
		return fmt.Sprintf("hook %s for tool %q failed: %v", e.Event, e.ToolName, e.Err)
	}
	return fmt.Sprintf("hook %s failed: %v", e.Event, e.Err)
}

// Unwrap returns the underlying hook callback error.
func (e *HookError) Unwrap() error { return e.Err }

// IsHookError checks if the error is a HookError anywhere in its chain.
func IsHookError(err error) bool {
	var he *HookError
	return errors.As(err, &he)
}
