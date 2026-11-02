package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"iter"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
)

const (
	defaultInitializeTimeout = 60 * time.Second
	defaultMessageBuffer     = 256
	seenCap                  = 4096
	exitWait                 = 5 * time.Second
	denyTimeout              = 2 * time.Second
)

type clientState int

const (
	stateOpen clientState = iota
	stateClosed
	stateDetached
)

// StreamError wraps a non-fatal problem with one stdout line (malformed
// JSON, a line over the size cap, too much nesting). The stream continues;
// the line is lost.
type StreamError struct{ Err error }

func (e *StreamError) Error() string { return "claude: bad stdout line: " + e.Err.Error() }
func (e *StreamError) Unwrap() error { return e.Err }

type msgItem struct {
	msg protocol.Message
	err error
}

type ctrlResult struct {
	resp *protocol.ControlResponse
	err  error
}

// Client drives one Claude Code CLI process over the stream-json protocol.
// It is safe for concurrent use, except that Messages and Turn share one
// queue: use a single consumer.
//
// Backpressure: stdout is read by one goroutine that pushes messages into a
// bounded queue (Options.MessageBuffer). It blocks only when that queue is
// full and nobody is consuming Messages; it never waits for callbacks, which
// run on their own goroutines. While the reader is blocked, control
// responses are not processed either, so keep consuming Messages while a
// turn runs.
type Client struct {
	opts     Options
	t        transport.Transport
	limits   protocol.Limits
	cleanups []func()

	rctx    context.Context // reader
	rcancel context.CancelFunc
	cbCtx   context.Context // callbacks; cancelled by Close and Detach
	cbStop  context.CancelFunc

	msgs       chan msgItem
	readerDone chan struct{}

	hookRegs map[string][]protocol.HookRegistration
	hookFns  map[string]HookFunc

	mu           sync.Mutex
	state        clientState
	ended        bool  // the reader has finished
	endErr       error // why; nil for a clean end or a user-initiated stop
	pending      map[string]chan ctrlResult
	inflight     map[string]*call
	seen         map[string]struct{}
	seenOrder    []string
	answers      map[string][]byte // replies written (or attempted), by request id
	answerOrder  []string
	sent         int // user messages sent
	delivered    int // results yielded by Messages
	sessionID    string
	server       *protocol.InitializeResponse
	lastResult   *protocol.Result
	pendingFound []*protocol.ControlRequest

	suppress    atomic.Bool // set while detaching: no reply may be written
	cleanupOnce sync.Once
	reqSeq      atomic.Uint64
	closeOnce   sync.Once
	closeErr    error
}

func newClient(opts Options) *Client {
	c := &Client{
		opts:       opts,
		limits:     opts.Limits,
		readerDone: make(chan struct{}),
		pending:    map[string]chan ctrlResult{},
		inflight:   map[string]*call{},
		seen:       map[string]struct{}{},
		answers:    map[string][]byte{},
		hookFns:    map[string]HookFunc{},
	}
	buf := opts.MessageBuffer
	if buf <= 0 {
		buf = defaultMessageBuffer
	}
	c.msgs = make(chan msgItem, buf)
	c.rctx, c.rcancel = context.WithCancel(context.Background())
	c.cbCtx, c.cbStop = context.WithCancel(context.Background())
	c.registerHooks()
	return c
}

// registerHooks assigns hook_N ids in a deterministic (event-sorted) order.
func (c *Client) registerHooks() {
	if len(c.opts.Hooks) == 0 {
		return
	}
	events := make([]string, 0, len(c.opts.Hooks))
	for ev := range c.opts.Hooks {
		events = append(events, string(ev))
	}
	sort.Strings(events)
	c.hookRegs = map[string][]protocol.HookRegistration{}
	n := 0
	for _, ev := range events {
		for _, m := range c.opts.Hooks[HookEvent(ev)] {
			reg := protocol.HookRegistration{Matcher: m.Matcher, Timeout: m.Timeout, HookCallbackIDs: []string{}}
			for _, fn := range m.Callbacks {
				id := fmt.Sprintf("hook_%d", n)
				n++
				c.hookFns[id] = fn
				reg.HookCallbackIDs = append(reg.HookCallbackIDs, id)
			}
			c.hookRegs[ev] = append(c.hookRegs[ev], reg)
		}
	}
}

// NewClient spawns the CLI (unless Options.Transport is set) and performs the
// initialize handshake, reading stdout meanwhile. ctx bounds startup only;
// the process lives until Close or Detach.
func NewClient(ctx context.Context, opts Options) (*Client, error) {
	c := newClient(opts)
	c.t = opts.Transport
	if c.t == nil {
		spec, err := LaunchSpec(opts)
		if err != nil {
			return nil, err
		}
		c.cleanups = append(c.cleanups, spec.Cleanup)
		lim := opts.Limits
		if lim.MaxLineBytes <= 0 {
			lim.MaxLineBytes = protocol.DefaultMaxLineBytes
		}
		c.t = &transport.Subprocess{Path: spec.Path, Args: spec.Args, Dir: spec.Dir, Env: spec.Env,
			EnvUnset: opts.EnvUnset, MaxLineBytes: lim.MaxLineBytes, OnStderr: opts.Stderr}
	} else if _, err := buildArgs(&opts, ""); err != nil {
		return nil, err
	}
	if err := c.t.Start(ctx); err != nil {
		c.runCleanups()
		return nil, err
	}
	go c.readLoop()
	if _, err := c.initialize(ctx); err != nil {
		_ = c.Close()
		return nil, err
	}
	if opts.Resume != "" || opts.Continue { // a resumed session may carry its own model and mode
		if opts.Model != "" {
			if err := c.SetModel(ctx, opts.Model); err != nil {
				_ = c.Close()
				return nil, err
			}
		}
		if opts.PermissionMode != "" {
			if err := c.SetPermissionMode(ctx, opts.PermissionMode); err != nil {
				_ = c.Close()
				return nil, err
			}
		}
	}
	return c, nil
}

// Attach takes over a CLI that is already running behind t (for example
// after a daemon restart; see Detach). It does not start t. It re-sends
// initialize, reads the pending_permission_requests the CLI reports next to
// the reply (outside the inner response) and runs each through
// Options.CanUseTool exactly like a fresh request, once per request_id.
// Options.Hooks are re-registered; a hook callback the CLI was waiting on
// is resolved by the CLI itself. Pending returns what was found.
func Attach(ctx context.Context, t transport.Transport, opts Options) (*Client, error) {
	if _, err := buildArgs(&opts, ""); err != nil {
		return nil, err
	}
	c := newClient(opts)
	c.t = t
	go c.readLoop()
	if _, err := c.initialize(ctx); err != nil {
		c.stopLocal(stateDetached)
		return nil, err
	}
	return c, nil
}

func (c *Client) runCleanups() {
	c.cleanupOnce.Do(func() {
		for _, f := range c.cleanups {
			f()
		}
	})
}

// ---- reader -------------------------------------------------------------

func (c *Client) pushMsg(it msgItem) bool {
	select {
	case c.msgs <- it:
		return true
	case <-c.rctx.Done():
		return false
	}
}

func (c *Client) readLoop() {
	var readErr error
loop:
	for {
		line, err := c.t.ReadLine(c.rctx)
		if err != nil {
			var tl *transport.LineTooLongError
			switch {
			case errors.As(err, &tl):
				if !c.pushMsg(msgItem{err: &StreamError{Err: err}}) {
					break loop
				}
				continue
			case errors.Is(err, io.EOF), c.rctx.Err() != nil:
			default:
				readErr = fmt.Errorf("claude: reading stdout: %w", err)
			}
			break
		}
		fr, err := protocol.Decode(line, c.limits)
		if err != nil {
			if errors.Is(err, protocol.ErrSkipLine) {
				continue
			}
			if !c.pushMsg(msgItem{err: &StreamError{Err: err}}) {
				break
			}
			continue
		}
		if !c.route(fr) {
			break
		}
	}
	c.finish(readErr)
}

// route handles one frame; it returns false when the reader should stop.
func (c *Client) route(fr protocol.Frame) bool {
	switch f := fr.(type) {
	case *protocol.ControlResponse:
		c.mu.Lock()
		ch, ok := c.pending[f.RequestID]
		delete(c.pending, f.RequestID)
		c.mu.Unlock()
		if ok {
			ch <- ctrlResult{resp: f}
		}
	case *protocol.ControlRequest:
		c.dispatch(f, false)
	case *protocol.ControlCancelRequest:
		c.mu.Lock()
		call := c.inflight[f.RequestID]
		c.mu.Unlock()
		if call != nil {
			call.abandon()
		}
	case *protocol.KeepAlive:
	case protocol.Message:
		c.track(f)
		return c.pushMsg(msgItem{msg: f})
	}
	return true
}

func (c *Client) track(m protocol.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch v := m.(type) {
	case *protocol.SystemInit:
		if v.SessionID != "" {
			c.sessionID = v.SessionID
		}
	case *protocol.ConversationReset:
		if v.NewConversationID != "" {
			c.sessionID = v.NewConversationID
		}
	case *protocol.Result:
		c.lastResult = v
		if c.sessionID == "" {
			c.sessionID = v.SessionID
		}
	}
}

// finish runs when the reader stops: it works out why, fails pending
// requests and closes the message queue.
func (c *Client) finish(readErr error) {
	c.mu.Lock()
	user := c.state != stateOpen
	last := c.lastResult
	c.mu.Unlock()

	var endErr error
	switch {
	case user:
	case readErr != nil:
		endErr = readErr
	default:
		ctx, cancel := context.WithTimeout(context.Background(), exitWait)
		st, err := c.t.Wait(ctx)
		cancel()
		switch {
		case err != nil:
			endErr = fmt.Errorf("claude: stdout closed but the process status is unknown: %w", err)
		case st.ExitCode == 0 && st.Signal == "":
		case st.ExitCode == 1 && last != nil && last.IsError:
			endErr = &ResultError{Result: last, ExitCode: 1}
		default:
			endErr = &ProcessError{ExitCode: st.ExitCode, Signal: st.Signal, StderrTail: st.StderrTail}
		}
	}

	c.mu.Lock()
	c.ended, c.endErr = true, endErr
	pend := c.pending
	c.pending = map[string]chan ctrlResult{}
	dead := c.deadErrLocked()
	c.mu.Unlock()
	for _, ch := range pend {
		ch <- ctrlResult{err: dead}
	}
	close(c.msgs)
	close(c.readerDone)
}

// deadErrLocked is the error for requests made after (or during) the end.
func (c *Client) deadErrLocked() error {
	switch {
	case c.state == stateClosed:
		return ErrClosed
	case c.state == stateDetached:
		return ErrDetached
	case c.endErr != nil:
		return c.endErr
	}
	return fmt.Errorf("claude: CLI stdout ended before the response arrived: %w", io.ErrUnexpectedEOF)
}

func (c *Client) endError() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.endErr
}

// ---- public: messages, send, accessors -------------------------------------

// Messages yields what the CLI emits: assistant, user, system, stream events,
// results and so on. Non-fatal stream problems arrive as *StreamError and
// iteration may continue. When the CLI exits the sequence ends; a final
// *ResultError or *ProcessError is yielded first if the exit was abnormal.
// If ctx ends first, ctx.Err() is yielded. All consumers share one queue.
func (c *Client) Messages(ctx context.Context) iter.Seq2[protocol.Message, error] {
	return func(yield func(protocol.Message, error) bool) {
		for {
			select {
			case it, ok := <-c.msgs:
				if !ok {
					if err := c.endError(); err != nil {
						yield(nil, err)
					}
					return
				}
				if _, isResult := it.msg.(*protocol.Result); isResult {
					c.mu.Lock()
					c.delivered++
					c.mu.Unlock()
				}
				if !yield(it.msg, it.err) {
					return
				}
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
	}
}

// Send writes a user message. It may be called while a turn runs; the CLI
// queues it. A uuid is stamped when m.UUID is empty.
func (c *Client) Send(ctx context.Context, m UserMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.UUID == "" {
		m.UUID = newUUID()
	}
	line, err := protocol.EncodeUser(m)
	if err != nil {
		return err
	}
	if err := c.write(line); err != nil {
		return err
	}
	c.mu.Lock()
	c.sent++
	c.mu.Unlock()
	return nil
}

// Turn sends m and drains messages until the turn's Result. observe, if
// given, sees every message including the Result. It assumes no other turn
// is in flight. An error Result is returned as a result, not as an error;
// the error return is for transport and process failures.
func (c *Client) Turn(ctx context.Context, m UserMessage, observe ...func(protocol.Message)) (*protocol.Result, error) {
	// Results still owed to earlier messages (for instance a Turn whose ctx
	// ended before its result arrived) are skipped, so this Turn never
	// returns a stale one.
	c.mu.Lock()
	skip := max(c.sent-c.delivered, 0)
	c.mu.Unlock()
	if err := c.Send(ctx, m); err != nil {
		return nil, err
	}
	for msg, err := range c.Messages(ctx) {
		if err != nil {
			var se *StreamError
			if errors.As(err, &se) {
				continue
			}
			return nil, err
		}
		for _, o := range observe {
			o(msg)
		}
		if r, ok := msg.(*protocol.Result); ok {
			if skip > 0 {
				skip--
				continue
			}
			return r, nil
		}
	}
	c.mu.Lock()
	st := c.state
	c.mu.Unlock()
	switch st {
	case stateClosed:
		return nil, ErrClosed
	case stateDetached:
		return nil, ErrDetached
	}
	return nil, fmt.Errorf("claude: CLI ended before the turn produced a result: %w", io.ErrUnexpectedEOF)
}

// SessionID is the current session id, tracked from system/init and
// conversation_reset; it is empty until the first turn starts.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// ServerInfo is the parsed initialize response, with unstable fields raw.
func (c *Client) ServerInfo() *protocol.InitializeResponse {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.server
}

// Pending returns the permission and elicitation requests the CLI reported
// as outstanding at the latest initialize (see Attach).
func (c *Client) Pending() []*protocol.ControlRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*protocol.ControlRequest(nil), c.pendingFound...)
}

func (c *Client) writableErr() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.state == stateClosed:
		return ErrClosed
	case c.state == stateDetached:
		return ErrDetached
	case c.ended:
		return c.deadErrLocked()
	}
	return nil
}

func (c *Client) write(line []byte) error {
	if err := c.writableErr(); err != nil {
		return err
	}
	if err := c.t.WriteLine(line); err != nil {
		if errors.Is(err, transport.ErrInputClosed) {
			return fmt.Errorf("%w: %w", ErrClosed, err)
		}
		return err
	}
	return nil
}

// CloseInput signals end of input. The CLI finishes outstanding work and
// exits; Messages then ends. Do not call it while approvals or hooks may
// still be needed: the CLI fails them with "Stream closed".
func (c *Client) CloseInput() error {
	if err := c.writableErr(); err != nil {
		return err
	}
	return c.t.CloseInput()
}

// ---- close / detach -----------------------------------------------------

// Close ends the session: every open can_use_tool request is denied with
// {behavior: deny, interrupt: true} (so the CLI does not retry), then the
// transport is closed (stdin EOF, grace, SIGTERM, SIGKILL of the process
// group). Callback contexts are cancelled. Idempotent; a no-op after Detach.
func (c *Client) Close() error {
	c.closeOnce.Do(func() { c.closeErr = c.close() })
	return c.closeErr
}

func (c *Client) close() error {
	c.mu.Lock()
	if c.state == stateDetached {
		c.mu.Unlock()
		return nil
	}
	c.state = stateClosed
	calls := make([]*call, 0, len(c.inflight))
	for _, call := range c.inflight {
		calls = append(calls, call)
	}
	c.mu.Unlock()

	denied := make(chan struct{})
	go func() { // bounded: a wedged writer must not hold Close hostage
		defer close(denied)
		for _, call := range calls {
			if call.subtype == "can_use_tool" && call.claim() {
				line, err := protocol.EncodeControlSuccess(call.id, protocol.DenyResponse{
					Message: "session closed", Interrupt: true, ToolUseID: call.toolUseID})
				if err == nil {
					_ = c.t.WriteLine(line)
				}
			}
		}
	}()
	select {
	case <-denied:
	case <-time.After(denyTimeout):
	}
	c.cbStop()
	var err error
	if c.t != nil {
		err = c.t.Close()
	}
	c.rcancel()
	<-c.readerDone
	c.runCleanups()
	return err
}

// Detach stops reading and writing, cancels callback contexts without
// answering their requests, and calls Transport.Detach, leaving the CLI
// running (a daemon upgrade: a new process calls Attach and recovers open
// approvals from the CLI). Transports that own the process, such as the
// default subprocess, return transport.ErrDetachUnsupported and the client
// stays usable. Close after Detach is a no-op.
func (c *Client) Detach() error {
	c.mu.Lock()
	if c.state != stateOpen {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	// No reply may reach the transport from here on, even from a callback that
	// returns while Transport.Detach runs; undone if the transport refuses.
	c.suppress.Store(true)
	if err := c.t.Detach(); err != nil {
		c.suppress.Store(false)
		return err
	}
	c.stopLocal(stateDetached)
	c.runCleanups()
	return nil
}

// stopLocal stops the client without touching the process.
func (c *Client) stopLocal(st clientState) {
	c.mu.Lock()
	if c.state != stateOpen {
		c.mu.Unlock()
		return
	}
	c.state = st
	calls := make([]*call, 0, len(c.inflight))
	for _, call := range c.inflight {
		calls = append(calls, call)
	}
	c.mu.Unlock()
	for _, call := range calls {
		call.abandon()
	}
	c.cbStop()
	c.rcancel()
	<-c.readerDone
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func randRead(b []byte) (int, error) { return rand.Read(b) }
