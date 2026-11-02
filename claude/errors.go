package claude

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
)

var (
	// ErrClosed is returned by operations on a Client after Close, or when
	// input was closed.
	ErrClosed = errors.New("claude: client closed")
	// ErrDetached is returned by operations on a Client after Detach.
	ErrDetached = errors.New("claude: client detached")
	// ErrInitializeTimeout means the CLI did not answer initialize in time.
	ErrInitializeTimeout = errors.New("claude: initialize timed out")
)

// ControlError is an error control_response from the CLI.
type ControlError struct {
	Subtype string // the request subtype that failed
	Message string
	Code    string // the CLI's error_code, if any
}

func (e *ControlError) Error() string {
	return fmt.Sprintf("claude: control request %q failed: %s", e.Subtype, e.Message)
}

// ProcessError means the CLI exited abnormally without a preceding error
// result (crash, bad flags, killed). StderrTail holds the last bytes of stderr.
type ProcessError struct {
	ExitCode   int
	Signal     string
	StderrTail string
}

func (e *ProcessError) Error() string {
	what := fmt.Sprintf("exit code %d", e.ExitCode)
	if e.Signal != "" {
		what = "signal " + e.Signal
	}
	if last := lastLine(e.StderrTail); last != "" {
		return fmt.Sprintf("claude: CLI exited (%s); last stderr line: %s", what, last)
	}
	return fmt.Sprintf("claude: CLI exited (%s)", what)
}

var secretRE = regexp.MustCompile(`(?i)(sk-ant-[A-Za-z0-9_-]+|bearer\s+\S+|["']?(?:authorization|[a-z_-]*(?:token|api[-_]?key|secret|passw(?:or)?d))["']?\s*[:=]\s*(?:"[^"]*"|'[^']*'|basic\s+\S+|\S+))`)

// lastLine returns the last non-empty line, secrets masked and cut to 200 bytes.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	l := strings.TrimSpace(lines[len(lines)-1])
	l = secretRE.ReplaceAllString(l, "<redacted>")
	if len(l) > 200 {
		l = l[:200] + "..."
	}
	return l
}

// ResultError means the CLI exited with code 1 after an error result (for
// example after an interrupted turn and end of input). It is not a transport
// failure: Result carries what the CLI reported.
type ResultError struct {
	Result   *protocol.Result
	ExitCode int
}

func (e *ResultError) Error() string {
	return fmt.Sprintf("claude: run ended with error result (subtype %q, terminal reason %q, exit code %d)",
		e.Result.Subtype, e.Result.TerminalReason, e.ExitCode)
}
