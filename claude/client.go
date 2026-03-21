package claude

import (
	"context"
	"crypto/rand"
	"fmt"
	"sync"

	"github.com/albertocavalcante/claude-agent-sdk-go/internal/transport"
)

// ClaudeClient manages a persistent Claude CLI session.
// Unlike Query() which spawns one subprocess per call, ClaudeClient
// maintains a session across multiple Query calls using --session-id.
//
// ClaudeClient is safe for concurrent reads of SessionID while a Query
// is running, but only one Query should be active at a time.
type ClaudeClient struct {
	opts      Options
	sessionID string
	mu        sync.Mutex
	hooks     []HookRegistration
	transport transport.Transport
}

// NewClient creates a new ClaudeClient with the given options.
// The client will generate a session ID on the first Query call,
// or use Options.SessionID if provided.
func NewClient(opts Options) *ClaudeClient {
	return &ClaudeClient{
		opts:  opts,
		hooks: opts.Hooks,
		sessionID: opts.SessionID,
	}
}

// newClientWithTransport creates a new ClaudeClient with a custom transport
// for testing. This allows injecting a MockTransport instead of spawning
// a real subprocess.
func newClientWithTransport(opts Options, t transport.Transport) *ClaudeClient {
	c := NewClient(opts)
	c.transport = t
	return c
}

// SessionID returns the current session ID.
// It is safe to call concurrently while a Query is running.
func (c *ClaudeClient) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// Query sends a prompt and returns a channel of messages.
// Each call resumes the same session (via --session-id).
// The channel is closed when the conversation ends or the context is cancelled.
//
// Hook callbacks registered in the Options are fired for each message.
func (c *ClaudeClient) Query(ctx context.Context, prompt string) <-chan MessageOrError {
	ch := make(chan MessageOrError, 10)

	go func() {
		defer close(ch)

		// Ensure we have a session ID.
		c.mu.Lock()
		if c.sessionID == "" {
			c.sessionID = generateSessionID()
		}
		sessionID := c.sessionID

		// Build options with the session ID.
		opts := c.opts
		opts.SessionID = sessionID
		c.mu.Unlock()

		// Select transport: use injected transport for tests, otherwise subprocess.
		var t transport.Transport
		if c.transport != nil {
			t = c.transport
		} else {
			t = &transport.SubprocessTransport{}
		}

		// Use queryWithTransport to get raw messages.
		rawCh := queryWithTransport(ctx, prompt, opts, t)

		// Create a hook runner to track tool mappings across this query.
		runner := newHookRunner(c.hooks)

		for moe := range rawCh {
			if moe.Err != nil {
				select {
				case ch <- moe:
				case <-ctx.Done():
					return
				}
				continue
			}

			// Capture session ID from the first ResultMessage if we generated one.
			if rm, ok := moe.Message.(*ResultMessage); ok && rm.SessionID != "" {
				c.mu.Lock()
				if c.sessionID == sessionID {
					// Only update if we still have the session ID we started with.
					// This captures the real session ID from the CLI.
					c.sessionID = rm.SessionID
				}
				c.mu.Unlock()
			}

			// Fire hooks.
			currentSessionID := c.SessionID()
			runner.fireHooks(ctx, currentSessionID, moe.Message)

			// Forward message to caller.
			select {
			case ch <- moe:
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

// generateSessionID creates a random UUID-like session ID.
func generateSessionID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback: this should never happen in practice.
		return "session-fallback"
	}
	// Format as UUID v4.
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
