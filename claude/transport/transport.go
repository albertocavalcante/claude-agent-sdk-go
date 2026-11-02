// Package transport carries newline-delimited JSON between the SDK and a
// Claude Code CLI process. Transport is the seam for tests (see
// transporttest) and for hosts that own the CLI's pipes elsewhere.
package transport

import (
	"context"
	"errors"
	"fmt"
)

// SDKVersion is exported to the CLI as CLAUDE_AGENT_SDK_VERSION.
const SDKVersion = "0.3.0"

// DefaultEntrypoint is the CLAUDE_CODE_ENTRYPOINT value Subprocess sets when
// the caller did not. The CLI recognises {sdk-cli, sdk-ts, sdk-py}; an
// unrecognised value may switch off SDK-specific behaviour, so a recognised
// one is used. sdk-cli is the closest in meaning: a non-interactive host
// driving the CLI. Override it with Env.
const DefaultEntrypoint = "sdk-cli"

var (
	// ErrDetachUnsupported is returned by Detach on transports that own the
	// process (Subprocess): detaching would orphan it.
	ErrDetachUnsupported = errors.New("transport: detach is not supported")
	// ErrInputClosed is returned by WriteLine after CloseInput or Close.
	ErrInputClosed = errors.New("transport: input closed")
	// ErrLineTooLong marks an oversized stdout line; see LineTooLongError.
	ErrLineTooLong = errors.New("transport: stdout line exceeds limit")
	// ErrOutputTruncated means stdout was force-closed after the process
	// exited because a descendant kept it open; trailing output may be missing.
	ErrOutputTruncated = errors.New("transport: stdout force-closed after process exit")
	// ErrDetached is returned by writes after Detach on transports that track it.
	ErrDetached = errors.New("transport: detached")
	// ErrNotStarted is returned when a method needs a started transport.
	ErrNotStarted = errors.New("transport: not started")
)

// LineTooLongError is returned (once per line) by ReadLine when a stdout line
// exceeded the cap. The line is discarded and the next ReadLine continues
// with the following line, so it is not fatal.
type LineTooLongError struct{ Limit int }

func (e *LineTooLongError) Error() string {
	return fmt.Sprintf("transport: stdout line exceeds %d bytes", e.Limit)
}

// Is makes errors.Is(err, ErrLineTooLong) work.
func (e *LineTooLongError) Is(target error) bool { return target == ErrLineTooLong }

// ExitStatus describes how the CLI process ended.
type ExitStatus struct {
	// ExitCode is the process exit code, or -1 when it died from a signal.
	ExitCode int
	// Signal names the terminating signal ("killed", "terminated", ...), "" otherwise.
	Signal string
	// StderrTail is the last bytes of stderr (see Subprocess.StderrBytes).
	StderrTail string
}

// Transport is a bidirectional NDJSON channel to one CLI process.
type Transport interface {
	// Start launches the process (or connects). It is called once, by NewClient.
	Start(ctx context.Context) error
	// ReadLine returns the next stdout line without its newline. It returns
	// io.EOF once stdout is closed, ctx.Err() if ctx ends first (the
	// transport stays usable) and a *LineTooLongError for an oversized line.
	ReadLine(ctx context.Context) ([]byte, error)
	// WriteLine writes one frame and appends '\n'. Writes are serialised, so
	// concurrent callers never interleave bytes.
	WriteLine(line []byte) error
	// CloseInput signals end of input (EOF on the CLI's stdin) and leaves the
	// process running to finish its work.
	CloseInput() error
	// Close terminates: close stdin, wait a grace period, SIGTERM the process
	// group, wait, SIGKILL it. Idempotent.
	Close() error
	// Detach stops using the transport and leaves the process running so a
	// new owner can attach. Transports that own the process return
	// ErrDetachUnsupported.
	Detach() error
	// Wait blocks until the process exits or ctx ends. A non-zero exit is
	// reported in ExitStatus, not as an error.
	Wait(ctx context.Context) (ExitStatus, error)
}
