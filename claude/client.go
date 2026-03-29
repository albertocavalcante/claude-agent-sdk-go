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
// When a new Query arrives while one is already in-flight, the client
// cancels the in-flight query and starts the new one immediately
// (cancel-and-replace). This prevents "session already in use" errors
// and provides snappy UX when users send messages rapidly.
type ClaudeClient struct {
	opts      Options
	sessionID string
	mu        sync.RWMutex
	hooks     []HookRegistration
	transport transport.Transport
	closed    bool

	// Active query tracking for cancel-and-replace.
	activeCancel context.CancelFunc // cancels the in-flight query
	activeDone   chan struct{}      // closed when the in-flight query goroutine exits
}

// NewClient creates a new ClaudeClient with the given options.
// The client will generate a session ID on the first Query call,
// or use Options.SessionID if provided.
func NewClient(opts Options) *ClaudeClient {
	return &ClaudeClient{
		opts:      opts,
		hooks:     opts.Hooks,
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
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.sessionID
}

// Close releases any resources held by the client.
// It cancels any in-flight query and waits for it to finish.
// After Close, the client should not be used.
func (c *ClaudeClient) Close() error {
	c.mu.Lock()
	c.closed = true
	cancel := c.activeCancel
	done := c.activeDone
	c.mu.Unlock()

	// Cancel in-flight query and wait for cleanup.
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	return nil
}

// Query sends a prompt and returns a channel of messages.
// Each call resumes the same session (via --session-id).
// The channel is closed when the conversation ends or the context is cancelled.
//
// If a previous query is still in-flight, it is cancelled before the new
// one starts. This ensures only one subprocess uses the session at a time.
//
// Hook callbacks registered in the Options are fired for each message.
func (c *ClaudeClient) Query(ctx context.Context, prompt string) <-chan MessageOrError {
	ch := make(chan MessageOrError, 10)

	go func() {
		defer close(ch)

		// Cancel any in-flight query and wait for it to finish.
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			ch <- MessageOrError{Err: fmt.Errorf("client is closed")}
			return
		}
		prevCancel := c.activeCancel
		prevDone := c.activeDone
		c.mu.Unlock()

		if prevCancel != nil {
			prevCancel()
		}
		if prevDone != nil {
			<-prevDone // wait for previous goroutine to exit
		}

		// Create a cancellable context for this query.
		queryCtx, queryCancel := context.WithCancel(ctx)
		done := make(chan struct{})

		// Register as the active query.
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			queryCancel()
			ch <- MessageOrError{Err: fmt.Errorf("client is closed")}
			return
		}
		c.activeCancel = queryCancel
		c.activeDone = done

		if c.sessionID == "" {
			c.sessionID = generateSessionID()
		}
		sessionID := c.sessionID

		// Copy options with the session ID.
		opts := c.opts
		opts.SessionID = sessionID

		// Select transport: use injected transport for tests, otherwise subprocess.
		var t transport.Transport
		if c.transport != nil {
			t = c.transport
		} else {
			t = &transport.SubprocessTransport{}
		}
		c.mu.Unlock()

		// Signal completion when this goroutine exits.
		defer func() {
			queryCancel()
			close(done)
		}()

		// Use queryWithTransport to get raw messages.
		rawCh := queryWithTransport(queryCtx, prompt, opts, t)

		// Create a hook runner to track tool mappings across this query.
		runner := newHookRunner(c.hooks)

		for moe := range rawCh {
			if moe.Err != nil {
				// Don't forward errors from cancelled queries.
				if queryCtx.Err() != nil {
					return
				}
				select {
				case ch <- moe:
				case <-queryCtx.Done():
					return
				}
				continue
			}

			// Capture session ID from the ResultMessage.
			if rm, ok := moe.Message.(*ResultMessage); ok && rm.SessionID != "" {
				c.mu.Lock()
				c.sessionID = rm.SessionID
				c.mu.Unlock()
			}

			// Fire hooks.
			currentSessionID := c.SessionID()
			runner.fireHooks(queryCtx, currentSessionID, moe.Message)

			// Forward message to caller.
			select {
			case ch <- moe:
			case <-queryCtx.Done():
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
