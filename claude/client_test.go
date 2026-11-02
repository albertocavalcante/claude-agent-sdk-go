package claude

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport/transporttest"
)

const defaultInitResponse = `{"commands":[],"models":[],"account":{},"pid":4242,"claude_code_version":"2.1.316","current_permission_mode":"default","capabilities":["ui_surface_v1"],"session_state":"idle"}`

// fakeCLI answers the SDK's control requests on a transporttest.Fake.
type fakeCLI struct {
	t        *testing.T
	f        *transporttest.Fake
	mu       sync.Mutex
	payloads map[string]string // subtype -> success payload; "ERR:msg" for an error reply
	silent   map[string]bool   // subtypes that get no answer
	outer    string            // extra envelope fields appended to initialize replies
}

func newFakeCLI(t *testing.T) *fakeCLI {
	t.Helper()
	c := &fakeCLI{t: t, f: transporttest.New(), payloads: map[string]string{"initialize": defaultInitResponse}, silent: map[string]bool{}}
	c.f.OnWrite(c.onWrite)
	return c
}

func (c *fakeCLI) set(subtype, payload string) {
	c.mu.Lock()
	c.payloads[subtype] = payload
	c.mu.Unlock()
}

func (c *fakeCLI) onWrite(line []byte) {
	var fr struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype string `json:"subtype"`
		} `json:"request"`
	}
	if json.Unmarshal(line, &fr) != nil || fr.Type != "control_request" {
		return
	}
	c.mu.Lock()
	p, ok := c.payloads[fr.Request.Subtype]
	silent := c.silent[fr.Request.Subtype]
	outer := c.outer
	c.mu.Unlock()
	if !ok || silent {
		return
	}
	if msg, isErr := strings.CutPrefix(p, "ERR:"); isErr {
		c.f.Feed(`{"type":"control_response","response":{"subtype":"error","request_id":"` + fr.RequestID + `","error":"` + msg + `"}}`)
		return
	}
	extra := ""
	if fr.Request.Subtype == "initialize" && outer != "" {
		extra = "," + outer
	}
	c.f.Feed(`{"type":"control_response","response":{"subtype":"success","request_id":"` + fr.RequestID + `","response":` + p + `}` + extra + `}`)
}

func (c *fakeCLI) request(id, requestJSON string) {
	c.f.Feed(`{"type":"control_request","request_id":"` + id + `","request":` + requestJSON + `}`)
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newClientT(t *testing.T, c *fakeCLI, opts Options) *Client {
	t.Helper()
	opts.Transport = c.f
	cl, err := NewClient(ctxT(t), opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	return cl
}

// awaitResponse polls the write log (without consuming it) for the
// control_response to requestID.
func awaitResponse(t *testing.T, f *transporttest.Fake, requestID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		for _, b := range f.Writes() {
			var fr struct {
				Type     string         `json:"type"`
				Response map[string]any `json:"response"`
			}
			if json.Unmarshal(b, &fr) == nil && fr.Type == "control_response" && fr.Response["request_id"] == requestID {
				return fr.Response
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no control_response for %s; writes: %q", requestID, f.Writes())
	return nil
}

func TestNewClientInitializes(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	first := c.f.Writes()[0]
	var req struct {
		Type      string          `json:"type"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	_ = json.Unmarshal(first, &req)
	if req.Type != "control_request" || req.RequestID == "" || !strings.Contains(string(req.Request), `"subtype":"initialize"`) {
		t.Fatalf("first write = %s", first)
	}
	si := cl.ServerInfo()
	if si == nil || si.PID != 4242 || si.ClaudeCodeVersion != "2.1.316" {
		t.Errorf("ServerInfo = %+v", si)
	}
}

func TestInitializeRegistersHooksWithSDKIDs(t *testing.T) {
	c := newFakeCLI(t)
	var called atomic.Int32
	fn := func(context.Context, HookInput) (HookOutput, error) { called.Add(1); return HookOutput{}, nil }
	newClientT(t, c, Options{Hooks: map[HookEvent][]HookMatcher{
		HookPreToolUse: {{Matcher: "Bash", Callbacks: []HookFunc{fn, fn}}, {Matcher: "Edit", Timeout: 7, Callbacks: []HookFunc{fn}}},
		HookStop:       {{Callbacks: []HookFunc{fn}}},
	}})
	var init struct {
		Request struct {
			Hooks map[string][]struct {
				Matcher string   `json:"matcher"`
				IDs     []string `json:"hookCallbackIds"`
				Timeout int      `json:"timeout"`
			} `json:"hooks"`
		} `json:"request"`
	}
	_ = json.Unmarshal(c.f.Writes()[0], &init)
	pre := init.Request.Hooks["PreToolUse"]
	if len(pre) != 2 || len(pre[0].IDs) != 2 || pre[1].Timeout != 7 || len(init.Request.Hooks["Stop"]) != 1 {
		t.Fatalf("hooks = %+v", init.Request.Hooks)
	}
	seen := map[string]bool{}
	for _, ev := range init.Request.Hooks {
		for _, m := range ev {
			for _, id := range m.IDs {
				if !strings.HasPrefix(id, "hook_") || seen[id] {
					t.Errorf("bad or duplicate id %q", id)
				}
				seen[id] = true
			}
		}
	}
	if len(seen) != 4 {
		t.Errorf("want 4 distinct ids, got %v", seen)
	}
	// a hook_callback with a registered id runs the function and answers
	c.request("h1", `{"subtype":"hook_callback","callback_id":"`+pre[0].IDs[0]+`","input":{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"}},"tool_use_id":"toolu_1"}`)
	resp := awaitResponse(t, c.f, "h1")
	if resp["subtype"] != "success" || called.Load() != 1 {
		t.Errorf("resp=%v called=%d", resp, called.Load())
	}
	c.request("h2", `{"subtype":"hook_callback","callback_id":"hook_99","input":{}}`)
	if r := awaitResponse(t, c.f, "h2"); r["subtype"] != "error" {
		t.Errorf("unknown callback id must be an error reply: %v", r)
	}
}

func TestInitializeTimeout(t *testing.T) {
	c := newFakeCLI(t)
	c.silent["initialize"] = true
	_, err := NewClient(ctxT(t), Options{Transport: c.f, InitializeTimeout: 100 * time.Millisecond})
	if !errors.Is(err, ErrInitializeTimeout) {
		t.Fatalf("err = %v", err)
	}
	if !contains2(c.f.Events(), "close") {
		t.Errorf("a failed NewClient must close the transport: %v", c.f.Events())
	}
}

func contains2(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestInitializeErrorReply(t *testing.T) {
	c := newFakeCLI(t)
	c.set("initialize", "ERR:nope")
	_, err := NewClient(ctxT(t), Options{Transport: c.f})
	var ce *ControlError
	if !errors.As(err, &ce) || ce.Subtype != "initialize" || ce.Message != "nope" {
		t.Fatalf("err = %v", err)
	}
}

func TestInitializeKeepsReadingStdout(t *testing.T) {
	// The CLI may talk to the SDK (e.g. an MCP handshake) before it answers
	// initialize; that request must be answered while we wait.
	c := newFakeCLI(t)
	c.silent["initialize"] = true
	done := make(chan error, 1)
	go func() {
		_, err := NewClient(ctxT(t), Options{Transport: c.f})
		done <- err
	}()
	if _, err := c.f.NextWrite(ctxT(t)); err != nil { // the initialize request
		t.Fatal(err)
	}
	c.request("early", `{"subtype":"mcp_message","server_name":"x","message":{}}`)
	if r := awaitResponse(t, c.f, "early"); r["subtype"] != "error" {
		t.Errorf("early request reply = %v", r)
	}
	c.f.Feed(`{"type":"control_response","response":{"subtype":"success","request_id":"` + firstRequestID(t, c.f) + `","response":` + defaultInitResponse + `}}`)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func firstRequestID(t *testing.T, f *transporttest.Fake) string {
	t.Helper()
	var m struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(f.Writes()[0], &m)
	return m.RequestID
}

func TestUnknownInboundSubtypeGetsErrorReply(t *testing.T) {
	c := newFakeCLI(t)
	newClientT(t, c, Options{})
	c.request("u1", `{"subtype":"brand_new_thing"}`)
	r := awaitResponse(t, c.f, "u1")
	if r["subtype"] != "error" || !strings.Contains(r["error"].(string), "brand_new_thing") {
		t.Errorf("reply = %v", r)
	}
	// the four subtypes TS deliberately leaves unanswered stay unanswered
	c.request("u2", `{"subtype":"remote_tool_call"}`)
	c.request("u3", `{"subtype":"another_unknown"}`)
	r = awaitResponse(t, c.f, "u3")
	if r["subtype"] != "error" {
		t.Errorf("reply = %v", r)
	}
	for _, w := range c.f.Writes() {
		if strings.Contains(string(w), `"u2"`) {
			t.Errorf("remote_tool_call must not be answered: %s", w)
		}
	}
}

func TestCanUseToolAllowEchoesInputAndToolUseID(t *testing.T) {
	c := newFakeCLI(t)
	var got ToolPermissionRequest
	newClientT(t, c, Options{CanUseTool: func(_ context.Context, r ToolPermissionRequest) (PermissionDecision, error) {
		got = r
		return Allow{}, nil
	}})
	c.request("p1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_9"}`)
	r := awaitResponse(t, c.f, "p1")
	inner := r["response"].(map[string]any)
	if inner["behavior"] != "allow" || inner["toolUseID"] != "toolu_9" {
		t.Errorf("reply = %v", inner)
	}
	if in, _ := json.Marshal(inner["updatedInput"]); string(in) != `{"command":"ls"}` {
		t.Errorf("unchanged input must be echoed, got %s", in)
	}
	if got.ToolName != "Bash" {
		t.Errorf("request = %+v", got)
	}
}

func TestCanUseToolAllowWithUpdates(t *testing.T) {
	c := newFakeCLI(t)
	newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
		return Allow{UpdatedInput: map[string]any{"command": "echo hi"},
			UpdatedPermissions: []PermissionUpdate{{Type: protocol.PermSetMode, Mode: "acceptEdits", Destination: "session"}}}, nil
	}})
	c.request("p1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"t"}`)
	inner := awaitResponse(t, c.f, "p1")["response"].(map[string]any)
	if b, _ := json.Marshal(inner["updatedInput"]); string(b) != `{"command":"echo hi"}` {
		t.Errorf("updatedInput = %s", b)
	}
	if b, _ := json.Marshal(inner["updatedPermissions"]); !strings.Contains(string(b), `"setMode"`) {
		t.Errorf("updatedPermissions = %s", b)
	}
}

func TestCanUseToolDenyAndHandlerError(t *testing.T) {
	c := newFakeCLI(t)
	var n atomic.Int32
	newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
		if n.Add(1) == 1 {
			return Deny{Message: "not today", Interrupt: true}, nil
		}
		return nil, errors.New("boom")
	}})
	c.request("d1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t1"}`)
	inner := awaitResponse(t, c.f, "d1")["response"].(map[string]any)
	if inner["behavior"] != "deny" || inner["message"] != "not today" || inner["interrupt"] != true || inner["toolUseID"] != "t1" {
		t.Errorf("deny reply = %v", inner)
	}
	c.request("d2", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t2"}`)
	if r := awaitResponse(t, c.f, "d2"); r["subtype"] != "error" || strings.Contains(r["error"].(string), "boom") {
		t.Errorf("handler error must become an error reply: %v", r)
	}
}

func TestCanUseToolPanicBecomesErrorReply(t *testing.T) {
	c := newFakeCLI(t)
	newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) { panic("kaboom") }})
	c.request("x", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}`)
	if r := awaitResponse(t, c.f, "x"); r["subtype"] != "error" {
		t.Errorf("reply = %v", r)
	}
}

func TestControlCancelRequestCancelsCallbackContext(t *testing.T) {
	c := newFakeCLI(t)
	started := make(chan struct{})
	cancelled := make(chan struct{})
	newClientT(t, c, Options{CanUseTool: func(ctx context.Context, _ ToolPermissionRequest) (PermissionDecision, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		return Allow{}, nil
	}})
	c.request("c1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}`)
	<-started
	c.f.Feed(`{"type":"control_cancel_request","request_id":"c1"}`)
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("callback context was not cancelled")
	}
	time.Sleep(50 * time.Millisecond)
	for _, w := range c.f.Writes() {
		if strings.Contains(string(w), `"c1"`) {
			t.Errorf("a cancelled request must get no response: %s", w)
		}
	}
}

func TestConcurrentCallbacks(t *testing.T) {
	c := newFakeCLI(t)
	var started sync.WaitGroup
	started.Add(2)
	release := make(chan struct{})
	newClientT(t, c, Options{CanUseTool: func(_ context.Context, r ToolPermissionRequest) (PermissionDecision, error) {
		started.Done()
		<-release // both callbacks must be running at once for this to return
		return Deny{Message: r.ToolUseID}, nil
	}})
	c.request("a", `{"subtype":"can_use_tool","tool_name":"X","input":{},"tool_use_id":"ta"}`)
	c.request("b", `{"subtype":"can_use_tool","tool_name":"X","input":{},"tool_use_id":"tb"}`)
	waitDone := make(chan struct{})
	go func() { started.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case <-time.After(5 * time.Second):
		t.Fatal("callbacks did not run concurrently")
	}
	close(release)
	for id, tool := range map[string]string{"a": "ta", "b": "tb"} {
		inner := awaitResponse(t, c.f, id)["response"].(map[string]any)
		if inner["message"] != tool {
			t.Errorf("response %s = %v", id, inner)
		}
	}
}

func TestReaderIsNotBlockedByASlowCallback(t *testing.T) {
	c := newFakeCLI(t)
	release := make(chan struct{})
	started := make(chan struct{})
	cl := newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
		close(started)
		<-release
		return Allow{}, nil
	}})
	defer close(release)
	c.request("slow", `{"subtype":"can_use_tool","tool_name":"X","input":{},"tool_use_id":"t"}`)
	<-started
	c.f.Feed(`{"type":"assistant","message":{"id":"m","content":[{"type":"text","text":"still flowing"}]}}`)
	msg, err := nextMessage(t, cl)
	if err != nil || msg.(*protocol.Assistant).Text() != "still flowing" {
		t.Fatalf("%v %v", msg, err)
	}
	// and control requests still complete
	c.set("mcp_status", `{"mcpServers":[]}`)
	if _, err := cl.MCPStatus(ctxT(t)); err != nil {
		t.Fatal(err)
	}
}

func nextMessage(t *testing.T, cl *Client) (protocol.Message, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for m, err := range cl.Messages(ctx) {
		return m, err
	}
	return nil, io.EOF
}

func TestPendingRequestsFailWhenReaderEnds(t *testing.T) {
	c := newFakeCLI(t)
	c.silent["mcp_status"] = true
	c.set("mcp_status", `{}`)
	cl := newClientT(t, c, Options{})
	errc := make(chan error, 1)
	go func() { _, err := cl.MCPStatus(ctxT(t)); errc <- err }()
	if _, err := c.f.AwaitWrite(ctxT(t), func(b []byte) bool { return strings.Contains(string(b), "mcp_status") }); err != nil {
		t.Fatal(err)
	}
	c.f.EOF(transport.ExitStatus{ExitCode: 0})
	select {
	case err := <-errc:
		if err == nil || !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending request not failed on EOF")
	}
	if _, err := cl.MCPStatus(ctxT(t)); err == nil {
		t.Error("requests after the reader ended must fail immediately")
	}
}

func TestCloseDeniesOpenRequestsBeforeClosingInput(t *testing.T) {
	c := newFakeCLI(t)
	started := make(chan struct{}, 2)
	var sawCancel atomic.Int32
	opts := Options{Transport: c.f, CanUseTool: func(ctx context.Context, _ ToolPermissionRequest) (PermissionDecision, error) {
		started <- struct{}{}
		<-ctx.Done()
		sawCancel.Add(1)
		return Allow{}, nil // late answer must be discarded
	}}
	cl, err := NewClient(ctxT(t), opts)
	if err != nil {
		t.Fatal(err)
	}
	c.request("o1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"ta"}`)
	c.request("o2", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"tb"}`)
	<-started
	<-started
	if err := cl.Close(); err != nil {
		t.Fatal(err)
	}
	ev := c.f.Events()
	closeAt := -1
	for i, e := range ev {
		if e == "close" || e == "close_input" {
			closeAt = i
			break
		}
	}
	if closeAt < 0 {
		t.Fatalf("transport never closed: %v", ev)
	}
	denies := map[string]bool{}
	for _, e := range ev[:closeAt] {
		if strings.Contains(e, `"behavior":"deny"`) && strings.Contains(e, `"interrupt":true`) {
			denies[e] = true
		}
	}
	if len(denies) != 2 {
		t.Fatalf("want 2 deny{interrupt:true} writes before closing input, events: %v", ev)
	}
	for _, e := range ev {
		if strings.Contains(e, `"behavior":"allow"`) {
			t.Errorf("late allow was written: %s", e)
		}
	}
	for deadline := time.Now().Add(5 * time.Second); sawCancel.Load() != 2 && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
	if sawCancel.Load() != 2 {
		t.Errorf("callback contexts cancelled = %d", sawCancel.Load())
	}
	if err := cl.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := cl.Send(ctxT(t), UserText("x")); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close = %v", err)
	}
}

func TestDetachWritesNothingAndCancelsCallbacks(t *testing.T) {
	c := newFakeCLI(t)
	started := make(chan struct{})
	ctxDone := make(chan struct{})
	cl, err := NewClient(ctxT(t), Options{Transport: c.f, CanUseTool: func(ctx context.Context, _ ToolPermissionRequest) (PermissionDecision, error) {
		close(started)
		<-ctx.Done()
		close(ctxDone)
		return Allow{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	c.request("dt", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}`)
	<-started
	before := len(c.f.Writes())
	if err := cl.Detach(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("callback context not cancelled by Detach")
	}
	time.Sleep(50 * time.Millisecond)
	if after := len(c.f.Writes()); after != before {
		t.Errorf("Detach wrote %d frame(s): %q", after-before, c.f.Writes()[before:])
	}
	ev := c.f.Events()
	if ev[len(ev)-1] != "detach" || contains2(ev, "close") || contains2(ev, "close_input") {
		t.Errorf("events = %v", ev)
	}
	if err := cl.Send(ctxT(t), UserText("x")); !errors.Is(err, ErrDetached) {
		t.Errorf("Send after Detach = %v", err)
	}
	if err := cl.Close(); err != nil { // Close after Detach is a no-op
		t.Errorf("Close after Detach: %v", err)
	}
	if contains2(c.f.Events(), "close") {
		t.Error("Close after Detach must not terminate the process")
	}
}

func TestAttachDispatchesOuterPendingRequestsOnce(t *testing.T) {
	c := newFakeCLI(t)
	c.outer = `"pending_permission_requests":[{"type":"control_request","request_id":"pend-1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"touch x"},"tool_use_id":"toolu_p"}}],"pending_user_dialog_requests":[]`
	var calls atomic.Int32
	cl, err := Attach(ctxT(t), c.f, Options{CanUseTool: func(_ context.Context, r ToolPermissionRequest) (PermissionDecision, error) {
		calls.Add(1)
		return Allow{}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	if contains2(c.f.Events(), "start") {
		t.Error("Attach must not start the transport")
	}
	inner := awaitResponse(t, c.f, "pend-1")["response"].(map[string]any)
	if inner["behavior"] != "allow" || inner["toolUseID"] != "toolu_p" {
		t.Errorf("reply = %v", inner)
	}
	// the same request arriving live (replay) is a duplicate
	c.request("pend-1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"toolu_p"}`)
	c.request("fresh", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"toolu_f"}`)
	awaitResponse(t, c.f, "fresh")
	if n := calls.Load(); n != 2 {
		t.Errorf("CanUseTool calls = %d, want 2 (pend-1 once, fresh once)", n)
	}
	n := 0
	for _, w := range c.f.Writes() {
		if strings.Contains(string(w), `"request_id":"pend-1"`) && strings.Contains(string(w), "control_response") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("responses for pend-1 = %d", n)
	}
	p := cl.Pending()
	if len(p) != 1 || p[0].RequestID != "pend-1" {
		t.Errorf("Pending() = %+v", p)
	}
}

func TestSendStampsUUIDAndOrigin(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	if err := cl.Send(ctxT(t), UserText("hello")); err != nil {
		t.Fatal(err)
	}
	m := c.f.Writes()[1]
	var u struct {
		Type    string `json:"type"`
		UUID    string `json:"uuid"`
		Session string `json:"session_id"`
		Origin  struct {
			Kind string `json:"kind"`
		} `json:"origin"`
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	}
	_ = json.Unmarshal(m, &u)
	if u.Type != "user" || len(u.UUID) != 36 || u.Origin.Kind != "human" || u.Session != "" || u.Message.Content != "hello" {
		t.Errorf("user frame = %s", m)
	}
	custom := UserText("x")
	custom.UUID = "custom-uuid"
	_ = cl.Send(ctxT(t), custom)
	if !strings.Contains(string(c.f.Writes()[2]), `"uuid":"custom-uuid"`) {
		t.Error("caller-chosen uuid must be kept")
	}
}

func TestTurnDrainsToResultAndTracksSession(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.OnWrite(func(line []byte) {
		c.onWrite(line)
		if strings.Contains(string(line), `"type":"user"`) {
			c.f.Feed(
				`{"type":"system","subtype":"init","session_id":"sess-1","model":"m"}`,
				`{"type":"assistant","message":{"id":"m1","content":[{"type":"text","text":"pong"}]}}`,
				`{"type":"result","subtype":"success","is_error":false,"result":"pong","session_id":"sess-1","num_turns":1,"total_cost_usd":0.5}`,
			)
		}
	})
	var seen []string
	res, err := cl.Turn(ctxT(t), UserText("ping"), func(m protocol.Message) { seen = append(seen, m.MessageType()) })
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != "pong" || res.TotalCostUSD != 0.5 {
		t.Errorf("result = %+v", res)
	}
	if cl.SessionID() != "sess-1" {
		t.Errorf("SessionID = %q", cl.SessionID())
	}
	if strings.Join(seen, ",") != "system,assistant,result" {
		t.Errorf("observed %v", seen)
	}
	c.f.Feed(`{"type":"conversation_reset","new_conversation_id":"sess-2"}`)
	deadline := time.Now().Add(2 * time.Second)
	for cl.SessionID() != "sess-2" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if cl.SessionID() != "sess-2" {
		t.Errorf("SessionID after reset = %q", cl.SessionID())
	}
}

func TestMessagesEndsWithResultErrorOnExit1(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.Feed(`{"type":"result","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_streaming","session_id":"s"}`)
	c.f.EOF(transport.ExitStatus{ExitCode: 1, StderrTail: "ignored"})
	var last error
	var n int
	for m, err := range cl.Messages(ctxT(t)) {
		if err != nil {
			last = err
			continue
		}
		if _, ok := m.(*protocol.Result); ok {
			n++
		}
	}
	var re *ResultError
	if n != 1 || !errors.As(last, &re) || re.ExitCode != 1 || re.Result.TerminalReason != "aborted_streaming" {
		t.Errorf("n=%d err=%v", n, last)
	}
}

func TestMessagesEndsWithProcessErrorOtherwise(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.EOF(transport.ExitStatus{ExitCode: 2, StderrTail: "bad flag"})
	var last error
	for _, err := range cl.Messages(ctxT(t)) {
		last = err
	}
	var pe *ProcessError
	if !errors.As(last, &pe) || pe.ExitCode != 2 || pe.StderrTail != "bad flag" {
		t.Errorf("err = %v", last)
	}
}

func TestMessagesEndsCleanlyOnExit0(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.EOF(transport.ExitStatus{})
	for m, err := range cl.Messages(ctxT(t)) {
		t.Fatalf("unexpected %v %v", m, err)
	}
}

func TestStreamErrorsAreNonFatal(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.Feed("[SandboxDebug] hello", `{"type":`, `{"type":"assistant","message":{"id":"m","content":[]}}`)
	var errs, msgs int
	ctx, cancel := context.WithTimeout(ctxT(t), 2*time.Second)
	defer cancel()
	for m, err := range cl.Messages(ctx) {
		switch {
		case errors.Is(err, context.Canceled):
		case err != nil:
			errs++
			var de *protocol.DecodeError
			if !errors.As(err, &de) {
				t.Errorf("err = %v", err)
			}
		case m != nil:
			msgs++
			cancel()
		}
	}
	if errs != 1 || msgs != 1 {
		t.Errorf("errs=%d msgs=%d (a stray non-JSON line is skipped, a broken JSON line is a non-fatal error)", errs, msgs)
	}
}

func TestControlAPIs(t *testing.T) {
	c := newFakeCLI(t)
	c.set("interrupt", `{"still_queued":["q1"]}`)
	c.set("set_permission_mode", `{"mode":"plan"}`)
	c.set("set_model", `{"model":"claude-haiku-5-5"}`)
	c.set("mcp_status", `{"mcpServers":[{"name":"s","status":"connected"}]}`)
	c.set("get_context_usage", `{"totalTokens":5}`)
	c.set("rewind_files", `{"canRewind":true,"filesChanged":["a"]}`)
	c.set("stop_task", `{}`)
	c.set("list_models", `{"models":[1]}`)
	c.set("rewind_bad", "ERR:File rewinding is not enabled.")
	cl := newClientT(t, c, Options{})
	ctx := ctxT(t)
	ir, err := cl.Interrupt(ctx)
	if err != nil || len(ir.StillQueued) != 1 {
		t.Errorf("Interrupt %+v %v", ir, err)
	}
	if err := cl.SetPermissionMode(ctx, ModePlan); err != nil {
		t.Error(err)
	}
	if err := cl.SetModel(ctx, "haiku"); err != nil {
		t.Error(err)
	}
	ms, err := cl.MCPStatus(ctx)
	if err != nil || ms.MCPServers[0].Status != "connected" {
		t.Errorf("MCPStatus %+v %v", ms, err)
	}
	cu, err := cl.ContextUsage(ctx)
	if err != nil || string(cu) != `{"totalTokens":5}` {
		t.Errorf("ContextUsage %s %v", cu, err)
	}
	rw, err := cl.RewindFiles(ctx, "uuid-1", true)
	if err != nil || !rw.CanRewind {
		t.Errorf("RewindFiles %+v %v", rw, err)
	}
	if err := cl.StopTask(ctx, "task-1"); err != nil {
		t.Error(err)
	}
	raw, err := cl.Request(ctx, "list_models", map[string]any{"x": 1})
	if err != nil || string(raw) != `{"models":[1]}` {
		t.Errorf("Request %s %v", raw, err)
	}
	// wire check for payload fields
	all := ""
	for _, w := range c.f.Writes() {
		all += string(w) + "\n"
	}
	for _, want := range []string{`"mode":"plan"`, `"model":"haiku"`, `"user_message_id":"uuid-1"`, `"dry_run":true`, `"task_id":"task-1"`, `"subtype":"list_models","x":1`} {
		if !strings.Contains(all, want) {
			t.Errorf("wire lacks %s", want)
		}
	}
	var ce *ControlError
	c.set("stop_task", "ERR:no such task")
	if err := cl.StopTask(ctx, "zzz"); !errors.As(err, &ce) || ce.Subtype != "stop_task" || ce.Message != "no such task" {
		t.Errorf("error reply = %v", err)
	}
}

func TestRequestContextCancel(t *testing.T) {
	c := newFakeCLI(t)
	c.silent["mcp_status"] = true
	c.set("mcp_status", `{}`)
	cl := newClientT(t, c, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := cl.MCPStatus(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
}

func TestResumeReappliesModelAndPermissionMode(t *testing.T) {
	c := newFakeCLI(t)
	c.set("set_model", `{"model":"x"}`)
	c.set("set_permission_mode", `{"mode":"plan"}`)
	newClientT(t, c, Options{Resume: "sess", Model: "haiku", PermissionMode: ModePlan})
	var subtypes []string
	for _, w := range c.f.Writes() {
		var fr struct {
			Request struct {
				Subtype string `json:"subtype"`
			} `json:"request"`
		}
		_ = json.Unmarshal(w, &fr)
		subtypes = append(subtypes, fr.Request.Subtype)
	}
	if strings.Join(subtypes, ",") != "initialize,set_model,set_permission_mode" {
		t.Errorf("writes = %v", subtypes)
	}
	// without Resume nothing is re-applied
	c2 := newFakeCLI(t)
	newClientT(t, c2, Options{Model: "haiku", PermissionMode: ModePlan})
	if len(c2.f.Writes()) != 1 {
		t.Errorf("fresh launch wrote %d frames", len(c2.f.Writes()))
	}
}

func TestQueryClosesInputImmediatelyWithoutCallbacks(t *testing.T) {
	c := newFakeCLI(t)
	c.f.OnWrite(func(line []byte) {
		c.onWrite(line)
		if strings.Contains(string(line), `"type":"user"`) {
			c.f.Feed(`{"type":"result","subtype":"success","is_error":false,"session_id":"s"}`)
			c.f.EOF(transport.ExitStatus{})
		}
	})
	var types []string
	for m, err := range Query(ctxT(t), "hi", Options{Transport: c.f}) {
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, m.MessageType())
	}
	if strings.Join(types, ",") != "result" {
		t.Errorf("types = %v", types)
	}
	ev := c.f.Events()
	if !contains2(ev, "close_input") && !contains2(ev, "close") {
		t.Errorf("events %v", ev)
	}
	userAt, closeAt := -1, -1
	for i, e := range ev {
		if strings.Contains(e, `"type":"user"`) && userAt < 0 {
			userAt = i
		}
		if e == "close_input" && closeAt < 0 {
			closeAt = i
		}
	}
	if closeAt < userAt || closeAt < 0 {
		t.Errorf("input must close right after the prompt: %v", ev)
	}
}

func TestQueryKeepsInputOpenUntilIdleWithCallbacks(t *testing.T) {
	c := newFakeCLI(t)
	c.f.OnWrite(func(line []byte) {
		c.onWrite(line)
		if strings.Contains(string(line), `"type":"user"`) {
			c.f.Feed(`{"type":"result","subtype":"success","is_error":false,"session_id":"s"}`)
		}
	})
	opts := Options{Transport: c.f, CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) { return Allow{}, nil }}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, err := range Query(ctxT(t), "hi", opts) {
			if err != nil {
				t.Error(err)
			}
		}
	}()
	// wait until the result has been handled, then check input is still open
	time.Sleep(150 * time.Millisecond)
	if contains2(c.f.Events(), "close_input") || contains2(c.f.Events(), "close") {
		t.Fatalf("input closed before idle: %v", c.f.Events())
	}
	c.f.Feed(`{"type":"system","subtype":"session_state_changed","state":"idle","sdk_host_only":true}`)
	deadline := time.Now().Add(5 * time.Second)
	for !contains2(c.f.Events(), "close_input") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !contains2(c.f.Events(), "close_input") {
		t.Fatal("input not closed after idle")
	}
	c.f.EOF(transport.ExitStatus{})
	<-done
}
