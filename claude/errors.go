package claude

import "fmt"

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
