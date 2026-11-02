package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
)

func (c *Client) newRequestID() string {
	var b [4]byte
	_, _ = randRead(b[:])
	return fmt.Sprintf("req_%d_%x", c.reqSeq.Add(1), b)
}

// roundTrip sends a control request and waits for its response. The reader
// keeps running meanwhile, so inbound requests are answered during the wait.
func (c *Client) roundTrip(ctx context.Context, subtype string, line func(id string) ([]byte, error)) (*protocol.ControlResponse, error) {
	id := c.newRequestID()
	ch := make(chan ctrlResult, 1)
	c.mu.Lock()
	switch {
	case c.state == stateClosed:
		c.mu.Unlock()
		return nil, ErrClosed
	case c.state == stateDetached:
		c.mu.Unlock()
		return nil, ErrDetached
	case c.ended:
		err := c.deadErrLocked()
		c.mu.Unlock()
		return nil, err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}

	b, err := line(id)
	if err != nil {
		forget()
		return nil, err
	}
	if err := c.t.WriteLine(b); err != nil {
		forget()
		return nil, err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		if r.resp.IsError() {
			return nil, &ControlError{Subtype: subtype, Message: r.resp.Error, Code: r.resp.ErrorCode}
		}
		return r.resp, nil
	case <-ctx.Done():
		forget()
		return nil, ctx.Err()
	}
}

func (c *Client) typed(ctx context.Context, p protocol.RequestPayload) (*protocol.ControlResponse, error) {
	return c.roundTrip(ctx, p.ControlSubtype(), func(id string) ([]byte, error) { return protocol.EncodeControlRequest(id, p) })
}

// Request sends any control request and returns the success payload. It is
// the escape hatch for subtypes this package has no method for. payload must
// marshal to a JSON object (or be nil).
func (c *Client) Request(ctx context.Context, subtype string, payload any) (json.RawMessage, error) {
	resp, err := c.roundTrip(ctx, subtype, func(id string) ([]byte, error) {
		return protocol.EncodeRawControlRequest(id, subtype, payload)
	})
	if err != nil {
		return nil, err
	}
	return resp.Response, nil
}

// Interrupt interrupts the running turn. The acknowledgement can arrive
// before the interrupted turn's Result (error_during_execution,
// terminal_reason aborted_streaming), so do not treat it as the end of the
// turn. The client stays usable.
func (c *Client) Interrupt(ctx context.Context) (*protocol.InterruptResponse, error) {
	resp, err := c.typed(ctx, &protocol.InterruptRequest{})
	if err != nil {
		return nil, err
	}
	return protocol.ParseInterruptResponse(orEmpty(resp.Response))
}

// SetPermissionMode changes the permission mode.
func (c *Client) SetPermissionMode(ctx context.Context, m PermissionMode) error {
	if !m.valid() {
		return invalid("permission mode %q", string(m))
	}
	_, err := c.typed(ctx, &protocol.SetPermissionModeRequest{Mode: string(m)})
	return err
}

// SetModel switches the model; "" selects the default.
func (c *Client) SetModel(ctx context.Context, model string) error {
	_, err := c.typed(ctx, &protocol.SetModelRequest{Model: model})
	return err
}

// MCPStatus reports the state of every MCP server.
func (c *Client) MCPStatus(ctx context.Context) (*protocol.MCPStatusResponse, error) {
	resp, err := c.typed(ctx, &protocol.MCPStatusRequest{})
	if err != nil {
		return nil, err
	}
	return protocol.ParseMCPStatusResponse(orEmpty(resp.Response))
}

// ContextUsage returns the raw get_context_usage payload (its shape is unstable).
func (c *Client) ContextUsage(ctx context.Context) (json.RawMessage, error) {
	resp, err := c.typed(ctx, &protocol.ContextUsageRequest{})
	if err != nil {
		return nil, err
	}
	return resp.Response, nil
}

// RewindFiles restores files to their state at a user message (the uuid from
// the replay echo or the one you set). It needs
// Options.EnableFileCheckpointing; otherwise the CLI answers "File rewinding
// is not enabled.".
func (c *Client) RewindFiles(ctx context.Context, userMessageUUID string, dryRun bool) (*protocol.RewindFilesResponse, error) {
	resp, err := c.typed(ctx, &protocol.RewindFilesRequest{UserMessageID: userMessageUUID, DryRun: dryRun})
	if err != nil {
		return nil, err
	}
	return protocol.ParseRewindFilesResponse(orEmpty(resp.Response))
}

// StopTask stops a background task by id.
func (c *Client) StopTask(ctx context.Context, taskID string) error {
	_, err := c.typed(ctx, &protocol.StopTaskRequest{TaskID: taskID})
	return err
}

// Reinitialize re-sends initialize to the running CLI (after a transport
// gap) and dispatches any pending requests it reports; see Attach.
func (c *Client) Reinitialize(ctx context.Context) (*protocol.InitializeResponse, error) {
	return c.initialize(ctx)
}

func orEmpty(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage(`{}`)
	}
	return r
}

func (c *Client) initialize(ctx context.Context) (*protocol.InitializeResponse, error) {
	timeout := c.opts.InitializeTimeout
	if timeout <= 0 {
		timeout = defaultInitializeTimeout
	}
	ictx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	resp, err := c.typed(ictx, &protocol.InitializeRequest{Hooks: c.hookRegs})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			return nil, ErrInitializeTimeout
		}
		return nil, err
	}
	ir, err := protocol.ParseInitializeResponse(orEmpty(resp.Response))
	if err != nil {
		return nil, fmt.Errorf("claude: bad initialize response: %w", err)
	}
	c.mu.Lock()
	c.server = ir
	if ir.SessionID != "" {
		c.sessionID = ir.SessionID
	}
	c.pendingFound = nil
	c.pendingFound = append(c.pendingFound, resp.PendingPermissionRequests...)
	c.pendingFound = append(c.pendingFound, resp.PendingElicitationRequests...)
	found := append([]*protocol.ControlRequest(nil), c.pendingFound...)
	c.mu.Unlock()
	for _, req := range found {
		c.dispatch(req, true) // same path as a fresh request; see dispatch for the dedupe rules
	}
	return ir, nil
}
