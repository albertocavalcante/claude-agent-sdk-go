// Package claude provides a Go SDK for the Claude Code CLI.
//
// It spawns the claude CLI as a subprocess and streams structured messages
// back over a channel, providing a Go-idiomatic interface for building
// agents and tools powered by Claude.
//
// Basic usage:
//
//	ctx := context.Background()
//	for msg := range claude.Query(ctx, "Hello, Claude!", claude.Options{}) {
//	    if msg.Err != nil {
//	        log.Fatal(msg.Err)
//	    }
//	    fmt.Println(msg.Message.Type())
//	}
package claude

import (
	"context"

	"github.com/albertocavalcante/claude-agent-sdk-go/internal/transport"
)

// MessageOrError carries either a parsed Message or an error from the
// transport layer. Callers should check Err first.
type MessageOrError struct {
	Message Message
	Err     error
}

// Query sends a one-shot prompt to Claude and returns a channel of messages.
// The channel is closed when the conversation ends or the context is cancelled.
//
// Each value on the channel is either a Message or an error. Callers should
// check MessageOrError.Err before accessing MessageOrError.Message.
func Query(ctx context.Context, prompt string, opts Options) <-chan MessageOrError {
	t := &transport.SubprocessTransport{}
	return queryWithTransport(ctx, prompt, opts, t)
}

// queryWithTransport is an internal helper for testing that accepts a
// custom transport instead of spawning a real subprocess.
func queryWithTransport(ctx context.Context, prompt string, opts Options, t transport.Transport) <-chan MessageOrError {
	ch := make(chan MessageOrError, 10)

	go func() {
		defer close(ch)

		tOpts := toTransportOptions(&opts)
		if err := t.Start(ctx, prompt, tOpts); err != nil {
			ch <- MessageOrError{Err: err}
			return
		}
		defer t.Close()

		for raw := range t.Lines() {
			if raw.Err != nil {
				select {
				case ch <- MessageOrError{Err: raw.Err}:
				case <-ctx.Done():
					return
				}
				continue
			}

			msg, err := ParseMessage(raw.Line)
			if err != nil {
				select {
				case ch <- MessageOrError{Err: err}:
				case <-ctx.Done():
					return
				}
				continue
			}

			select {
			case ch <- MessageOrError{Message: msg}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

// toTransportOptions converts public Options to the internal transport Options.
func toTransportOptions(opts *Options) *transport.Options {
	if opts == nil {
		return nil
	}
	return &transport.Options{
		Model:              opts.Model,
		SystemPrompt:       opts.SystemPrompt,
		AppendSystemPrompt: opts.AppendSystemPrompt,
		AllowedTools:       opts.AllowedTools,
		DisallowedTools:    opts.DisallowedTools,
		MaxThinkingTokens:  opts.MaxThinkingTokens,
		MaxTurns:           opts.MaxTurns,
		WorkingDirectory:   opts.WorkingDirectory,
		PermissionMode:     opts.PermissionMode,
		CLIPath:            opts.CLIPath,
		Env:                opts.Env,
	}
}
