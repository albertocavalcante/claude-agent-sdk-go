// Package transport defines the interface for communicating with the Claude CLI.
// It operates at the byte level, returning raw JSON lines. Parsing into typed
// messages is handled by the caller (the claude package).
package transport

import (
	"context"
)

// Options holds the configuration needed by the transport layer to spawn
// the CLI subprocess. This is a transport-internal copy of the public
// claude.Options to avoid import cycles.
type Options struct {
	Model              string
	SystemPrompt       string
	AppendSystemPrompt string
	AllowedTools       []string
	DisallowedTools    []string
	MaxThinkingTokens  int
	MaxTurns           int
	WorkingDirectory   string
	PermissionMode     string
	CLIPath            string
	CLIPrefixArgs      []string
	Env                map[string]string
	SessionID          string
	MCPConfigPath      string
}

// RawLineOrError carries either a raw JSON line from the CLI's stdout or an error.
type RawLineOrError struct {
	Line []byte
	Err  error
}

// Transport is the interface for sending prompts to the Claude CLI
// and receiving raw JSON lines back.
type Transport interface {
	// Start launches the CLI process with the given prompt and options.
	// It must be called exactly once before reading from Lines().
	Start(ctx context.Context, prompt string, opts *Options) error

	// Lines returns a channel that yields raw JSON lines (or errors)
	// from the CLI process. The channel is closed when the process exits.
	Lines() <-chan RawLineOrError

	// Close terminates the CLI process and releases resources.
	Close() error
}
