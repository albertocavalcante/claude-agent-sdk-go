package claude

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
)

// call is one inbound control request being handled. It is answered at
// most once: claim wins the right to write the response, and abandon (a
// control_cancel_request or Detach) claims it without writing.
type call struct {
	id        string
	subtype   string
	toolUseID string
	cancel    context.CancelFunc
	claimed   atomic.Bool
}

func (c *call) claim() bool { return c.claimed.CompareAndSwap(false, true) }

func (c *call) abandon() {
	c.claim()
	c.cancel()
}

// The subtypes the TypeScript SDK deliberately leaves unanswered.
var unansweredSubtypes = map[string]bool{
	"remote_tool_call":        true,
	"remote_plumbing_call":    true,
	"remote_tools_probe":      true,
	"remote_tools_reannounce": true,
}

// dispatch starts a goroutine for an inbound request.
//
// A request still being handled is never started twice. A request that
// arrives live again (a replay) after it was handled is ignored. A request
// the CLI lists as pending (fromPending, from an initialize reply) is by
// definition unanswered as far as the CLI knows: if its reply was already
// produced it is written again, otherwise (cancelled, or never answered) the
// callback runs again.
func (c *Client) dispatch(req *protocol.ControlRequest, fromPending bool) {
	if unansweredSubtypes[req.Subtype] {
		return
	}
	payload, perr := req.Payload()
	c.mu.Lock()
	if c.state != stateOpen {
		c.mu.Unlock()
		return
	}
	if _, running := c.inflight[req.RequestID]; running {
		c.mu.Unlock()
		return
	}
	if fromPending {
		if line, ok := c.answers[req.RequestID]; ok {
			c.mu.Unlock()
			if !c.suppress.Load() {
				c.write2(req.RequestID, line)
			}
			return
		}
	} else if _, dup := c.seen[req.RequestID]; dup {
		c.mu.Unlock()
		return
	}
	if _, known := c.seen[req.RequestID]; !known {
		c.seen[req.RequestID] = struct{}{}
		c.seenOrder = append(c.seenOrder, req.RequestID)
		if len(c.seenOrder) > seenCap {
			delete(c.seen, c.seenOrder[0])
			c.seenOrder = c.seenOrder[1:]
		}
	}
	ctx, cancel := context.WithCancel(c.cbCtx)
	cl := &call{id: req.RequestID, subtype: req.Subtype, cancel: cancel}
	if cu, ok := payload.(*protocol.CanUseToolRequest); ok {
		cl.toolUseID = cu.ToolUseID
	}
	c.inflight[cl.id] = cl
	c.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				c.replyError(cl, "callback failed")
			}
			cancel()
			c.mu.Lock()
			delete(c.inflight, cl.id)
			c.mu.Unlock()
		}()
		c.run(ctx, cl, req, payload, perr)
	}()
}

func (c *Client) run(ctx context.Context, cl *call, req *protocol.ControlRequest, payload any, perr error) {
	if perr != nil {
		c.replyError(cl, "invalid "+req.Subtype+" request")
		return
	}
	switch p := payload.(type) {
	case *protocol.CanUseToolRequest:
		c.runCanUseTool(ctx, cl, p)
	case *protocol.HookCallbackRequest:
		c.runHook(ctx, cl, p)
	case *protocol.ElicitationRequest:
		res := ElicitationResult{Action: "decline"}
		if c.opts.OnElicitation != nil {
			var err error
			if res, err = c.opts.OnElicitation(ctx, *p); err != nil {
				c.replyError(cl, callbackText(err, "elicitation callback failed"))
				return
			}
		}
		c.replySuccess(cl, res)
	default:
		c.replyError(cl, "Unsupported control request subtype: "+req.Subtype)
	}
}

func (c *Client) runCanUseTool(ctx context.Context, cl *call, p *protocol.CanUseToolRequest) {
	if c.opts.CanUseTool == nil {
		c.replySuccess(cl, protocol.DenyResponse{Message: "no permission handler configured", ToolUseID: p.ToolUseID})
		return
	}
	dec, err := c.opts.CanUseTool(ctx, *p)
	if err != nil {
		c.replyError(cl, callbackText(err, "permission callback failed"))
		return
	}
	switch d := dec.(type) {
	case *Allow:
		dec = *d
	case *Deny:
		dec = *d
	}
	switch d := dec.(type) {
	case Allow:
		in, merr := allowInput(d.UpdatedInput, p.Input)
		if merr != nil {
			c.replyError(cl, "cannot encode updated input: "+merr.Error())
			return
		}
		c.replySuccess(cl, protocol.AllowResponse{UpdatedInput: in, UpdatedPermissions: d.UpdatedPermissions, ToolUseID: p.ToolUseID})
	case Deny:
		c.replySuccess(cl, protocol.DenyResponse{Message: d.Message, Interrupt: d.Interrupt, ToolUseID: p.ToolUseID})
	default:
		c.replyError(cl, "permission handler returned no decision")
	}
}

// allowInput returns the updatedInput to send: the caller's, or the original.
func allowInput(updated any, original json.RawMessage) (json.RawMessage, error) {
	if updated == nil {
		if len(original) == 0 {
			return json.RawMessage(`{}`), nil
		}
		return original, nil
	}
	if raw, ok := updated.(json.RawMessage); ok {
		return raw, nil
	}
	return json.Marshal(updated)
}

func (c *Client) runHook(ctx context.Context, cl *call, p *protocol.HookCallbackRequest) {
	fn, ok := c.hookFns[p.CallbackID]
	if !ok {
		c.replyError(cl, "unknown hook callback id")
		return
	}
	var hi struct {
		Event          string          `json:"hook_event_name"`
		SessionID      string          `json:"session_id"`
		TranscriptPath string          `json:"transcript_path"`
		CWD            string          `json:"cwd"`
		PermissionMode string          `json:"permission_mode"`
		ToolName       string          `json:"tool_name"`
		ToolInput      json.RawMessage `json:"tool_input"`
		ToolUseID      string          `json:"tool_use_id"`
		AgentID        string          `json:"agent_id"`
	}
	_ = json.Unmarshal(p.Input, &hi)
	if p.ToolUseID != "" {
		hi.ToolUseID = p.ToolUseID
	}
	out, err := fn(ctx, HookInput{Event: HookEvent(hi.Event), SessionID: hi.SessionID, TranscriptPath: hi.TranscriptPath,
		CWD: hi.CWD, PermissionMode: hi.PermissionMode, ToolName: hi.ToolName, ToolInput: hi.ToolInput,
		ToolUseID: hi.ToolUseID, AgentID: hi.AgentID, Raw: p.Input})
	if err != nil {
		c.replyError(cl, callbackText(err, "hook callback failed"))
		return
	}
	c.replySuccess(cl, out)
}

// callbackText is the error text sent to the CLI for a failed callback:
// generic, unless the callback returned a *UserError.
func callbackText(err error, generic string) string {
	var ue *UserError
	if errors.As(err, &ue) {
		return ue.Message
	}
	return generic
}

func (c *Client) replySuccess(cl *call, payload any) {
	if !cl.claim() {
		return
	}
	if line, err := protocol.EncodeControlSuccess(cl.id, payload); err == nil {
		c.deliver(cl.id, line)
	}
}

func (c *Client) replyError(cl *call, msg string) {
	if !cl.claim() {
		return
	}
	if line, err := protocol.EncodeControlError(cl.id, msg); err == nil {
		c.deliver(cl.id, line)
	}
}

// deliver caches the reply (so a pending re-listing can be answered again),
// then writes it unless the client is detaching. A write failure is reported
// to Options.OnReplyError.
func (c *Client) deliver(id string, line []byte) {
	c.mu.Lock()
	if _, ok := c.answers[id]; !ok {
		c.answerOrder = append(c.answerOrder, id)
		if len(c.answerOrder) > seenCap {
			delete(c.answers, c.answerOrder[0])
			c.answerOrder = c.answerOrder[1:]
		}
	}
	c.answers[id] = line
	c.mu.Unlock()
	if c.suppress.Load() {
		return
	}
	c.write2(id, line)
}

func (c *Client) write2(id string, line []byte) {
	if err := c.t.WriteLine(line); err != nil && c.opts.OnReplyError != nil {
		c.opts.OnReplyError(id, err)
	}
}
