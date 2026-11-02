package claude

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport/transporttest"
)

// M1: a numeric error_code must not make the response unroutable.
func TestNumericErrorCodeRoutes(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.OnWrite(func(line []byte) {
		if s := string(line); strings.Contains(s, `"set_model"`) {
			c.f.Feed(`{"type":"control_response","response":{"subtype":"error","request_id":"` + reqID(s) + `","error":"nope","error_code":42}}`)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var ce *ControlError
	if err := cl.SetModel(ctx, "x"); !errors.As(err, &ce) || ce.Code != "42" || ce.Message != "nope" {
		t.Errorf("err = %v", err)
	}
}

func reqID(frame string) string {
	i := strings.Index(frame, `"request_id":"`) + len(`"request_id":"`)
	return frame[i : i+strings.Index(frame[i:], `"`)]
}

// M2: a request the CLI still lists as pending after Reinitialize is
// answered again, from the cached answer or by re-running the callback.
func TestReinitializeReanswersPendingRequests(t *testing.T) {
	c := newFakeCLI(t)
	var calls atomic.Int32
	cl := newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
		calls.Add(1)
		return Allow{}, nil
	}})
	body := `{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_1"}`
	c.request("perm-1", body)
	awaitResponse(t, c.f, "perm-1")
	pendingReply := func() {
		c.f.OnWrite(func(line []byte) {
			if s := string(line); strings.Contains(s, `"initialize"`) {
				c.f.Feed(`{"type":"control_response","response":{"subtype":"success","request_id":"` + reqID(s) + `","response":` + defaultInitResponse + `},"pending_permission_requests":[{"type":"control_request","request_id":"perm-1","request":` + body + `}]}`)
			}
		})
	}
	pendingReply()
	before := len(c.f.Writes())
	if _, err := cl.Reinitialize(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	count := func() (n int) {
		for _, w := range c.f.Writes()[before:] {
			if strings.Contains(string(w), `"request_id":"perm-1"`) && strings.Contains(string(w), "control_response") {
				n++
			}
		}
		return
	}
	for count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if count() != 1 || calls.Load() != 1 {
		t.Errorf("re-answers=%d callback calls=%d, want the cached answer resent once without re-asking", count(), calls.Load())
	}

	// a request that was cancelled (never answered) and is listed again runs again
	c.request("perm-2", strings.Replace(body, "toolu_1", "toolu_2", 1))
	awaitResponse(t, c.f, "perm-2")
	c.f.Feed(`{"type":"control_cancel_request","request_id":"perm-3"}`)
	c.request("perm-3", strings.Replace(body, "toolu_1", "toolu_3", 1))
	c.f.Feed(`{"type":"control_cancel_request","request_id":"perm-3"}`)
	time.Sleep(50 * time.Millisecond)
}

func TestReplyWriteErrorsAreSurfaced(t *testing.T) {
	c := newFakeCLI(t)
	var got atomic.Value
	newClientT(t, c, Options{
		CanUseTool:   func(context.Context, ToolPermissionRequest) (PermissionDecision, error) { return Allow{}, nil },
		OnReplyError: func(id string, err error) { got.Store(id + ": " + err.Error()) },
	})
	_ = c.f.CloseInput() // replies can no longer be written
	c.request("w1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}`)
	deadline := time.Now().Add(3 * time.Second)
	for got.Load() == nil && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if v, _ := got.Load().(string); !strings.HasPrefix(v, "w1: ") {
		t.Errorf("OnReplyError = %v", got.Load())
	}
}

// M3 at the client level: a drifted result still ends the turn, a drifted
// init still sets the session id.
func TestDriftedFramesStillDriveTheClient(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	c.f.OnWrite(func(line []byte) {
		if strings.Contains(string(line), `"type":"user"`) {
			c.f.Feed(`{"type":"system","subtype":"init","session_id":"sess-9","tools":[{"name":"Bash"}]}`,
				`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"sess-9","errors":[{"message":"boom"}]}`)
		}
	})
	res, err := cl.Turn(ctxT(t), UserText("hi"))
	if err != nil || res == nil || !res.IsError || cl.SessionID() != "sess-9" {
		t.Fatalf("res=%+v err=%v sid=%q", res, err, cl.SessionID())
	}
}

func TestCloseAndDetachRace(t *testing.T) {
	for range 40 {
		c := newFakeCLI(t)
		cl := newClientT(t, c, Options{})
		cl.cleanups = append(cl.cleanups, func() {})
		done := make(chan struct{})
		go func() { _ = cl.Detach(); close(done) }()
		_ = cl.Close()
		<-done
	}
}

// blockingWrites is a transport whose writes hang until it is closed.
type blockingWrites struct {
	*transporttest.Fake
	block   atomic.Bool
	release chan struct{}
}

func (b *blockingWrites) WriteLine(l []byte) error {
	if b.block.Load() {
		<-b.release
		return transport.ErrInputClosed
	}
	return b.Fake.WriteLine(l)
}

func (b *blockingWrites) Close() error {
	select {
	case <-b.release:
	default:
		close(b.release)
	}
	return b.Fake.Close()
}

func TestCloseBoundsTheDenyWrites(t *testing.T) {
	c := newFakeCLI(t)
	bw := &blockingWrites{Fake: c.f, release: make(chan struct{})}
	started := make(chan struct{})
	cl, err := NewClient(ctxT(t), Options{Transport: bw, CanUseTool: func(ctx context.Context, _ ToolPermissionRequest) (PermissionDecision, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	if err != nil {
		t.Fatal(err)
	}
	c.request("b1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}`)
	<-started
	bw.block.Store(true)
	start := time.Now()
	done := make(chan struct{})
	go func() { _ = cl.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close hung on a blocked deny write")
	}
	if d := time.Since(start); d > 6*time.Second {
		t.Errorf("Close took %v", d)
	}
}

func TestDetachSuppressesRepliesAndRejectsWrites(t *testing.T) {
	c := newFakeCLI(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var replyErr atomic.Int32
	cl, err := NewClient(ctxT(t), Options{Transport: c.f,
		OnReplyError: func(string, error) { replyErr.Add(1) },
		CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
			close(started)
			<-release // returns only after Detach: the answer must go nowhere
			return Allow{}, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	c.request("d1", `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"t"}`)
	<-started
	before := len(c.f.Writes())
	if err := cl.Detach(); err != nil {
		t.Fatal(err)
	}
	close(release)
	time.Sleep(100 * time.Millisecond)
	if len(c.f.Writes()) != before || replyErr.Load() != 0 {
		t.Errorf("writes %d->%d, reply errors %d", before, len(c.f.Writes()), replyErr.Load())
	}
	if err := c.f.WriteLine([]byte("x")); !errors.Is(err, transport.ErrDetached) {
		t.Errorf("the fake must reject writes after Detach, got %v", err)
	}
}

func TestAttachTakesSessionIDFromInitialize(t *testing.T) {
	c := newFakeCLI(t)
	c.set("initialize", `{"session_id":"sess-attached","pid":1}`)
	cl, err := Attach(ctxT(t), c.f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	if cl.SessionID() != "sess-attached" {
		t.Errorf("SessionID = %q", cl.SessionID())
	}
}

// A Turn abandoned by its context must not hand its late result to the next Turn.
func TestTurnAfterCancelledTurnSkipsStaleResult(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := cl.Turn(ctx, UserText("first")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Turn = %v", err)
	}
	c.f.OnWrite(func(line []byte) {
		if strings.Contains(string(line), `"second"`) {
			c.f.Feed(`{"type":"result","subtype":"success","result":"stale","session_id":"s"}`,
				`{"type":"result","subtype":"success","result":"fresh","session_id":"s"}`)
		}
	})
	res, err := cl.Turn(ctxT(t), UserText("second"))
	if err != nil || res.Result != "fresh" {
		t.Errorf("res=%+v err=%v", res, err)
	}
}

func TestProcessErrorDoesNotEmbedTheStderrTail(t *testing.T) {
	tail := "line one\nAuthorization: Bearer sk-ant-oat01-SECRETSECRET\n" + strings.Repeat("x", 5000)
	e := &ProcessError{ExitCode: 2, StderrTail: tail}
	msg := e.Error()
	if len(msg) > 400 || strings.Contains(msg, "SECRETSECRET") || strings.Contains(msg, "sk-ant-") {
		t.Errorf("Error() = %q", msg)
	}
	if e.StderrTail != tail {
		t.Error("the full tail stays available as a field")
	}
}

func TestCallbackErrorsAreGenericUnlessUserFacing(t *testing.T) {
	c := newFakeCLI(t)
	var n atomic.Int32
	newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
		if n.Add(1) == 1 {
			return nil, errors.New("db password is hunter2")
		}
		return nil, NewUserError("policy forbids this tool")
	}})
	c.request("e1", `{"subtype":"can_use_tool","tool_name":"X","input":{},"tool_use_id":"t"}`)
	r := awaitResponse(t, c.f, "e1")
	if r["subtype"] != "error" || strings.Contains(r["error"].(string), "hunter2") {
		t.Errorf("internal error text leaked to the CLI: %v", r)
	}
	c.request("e2", `{"subtype":"can_use_tool","tool_name":"X","input":{},"tool_use_id":"t"}`)
	if r := awaitResponse(t, c.f, "e2"); r["error"] != "policy forbids this tool" {
		t.Errorf("user-facing error: %v", r)
	}
}

// M4
func TestLaunchSpecAppliesTheSafeDefaults(t *testing.T) {
	spec, err := LaunchSpec(Options{
		Model: "haiku", Cwd: t.TempDir(), EnvUnset: []string{"CLAUDE_CODE_OAUTH_TOKEN"},
		Env:        []string{"PATH=/bin", "CLAUDE_CODE_OAUTH_TOKEN=secret", "CLAUDECODE=1"},
		MCPServers: nil, EnableFileCheckpointing: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer spec.Cleanup()
	if spec.Path != "claude" || spec.Dir == "" {
		t.Errorf("%+v", spec)
	}
	if !contains(spec.Args, "--model=haiku") || !contains(spec.Args, "--setting-sources=user") {
		t.Errorf("args = %q", spec.Args)
	}
	env := strings.Join(spec.Env, "\n")
	for _, bad := range []string{"CLAUDE_CODE_OAUTH_TOKEN=", "CLAUDECODE="} {
		if strings.Contains(env, bad) {
			t.Errorf("env still has %s", bad)
		}
	}
	for _, want := range []string{"CLAUDE_CODE_ENTRYPOINT=", "CLAUDE_AGENT_SDK_VERSION=", "CLAUDE_CODE_SDK_READS_SESSION_STATE=1", "CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=true", "PWD="} {
		if !strings.Contains(env, want) {
			t.Errorf("env lacks %s: %s", want, env)
		}
	}
}

func TestLaunchSpecWritesTheMCPConfigAndCleansUp(t *testing.T) {
	spec, err := LaunchSpec(Options{MCPServers: map[string]config.MCPServer{"a": {Command: "x"}}})
	if err != nil {
		t.Fatal(err)
	}
	var path string
	for _, a := range spec.Args {
		if p, ok := strings.CutPrefix(a, "--mcp-config="); ok {
			path = p
		}
	}
	if _, err := os.Stat(path); path == "" || err != nil {
		t.Fatalf("mcp config %q: %v", path, err)
	}
	spec.Cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Cleanup did not remove the MCP config")
	}
	if _, err := LaunchSpec(Options{ForkSession: true}); !errors.Is(err, ErrInvalidOptions) {
		t.Errorf("invalid options: %v", err)
	}
}

// Query with callbacks closes input after the BG wait ceiling when no idle arrives.
func TestQueryIdleFallback(t *testing.T) {
	c := newFakeCLI(t)
	c.f.OnWrite(func(line []byte) {
		c.onWrite(line)
		if strings.Contains(string(line), `"type":"user"`) {
			c.f.Feed(`{"type":"result","subtype":"success","is_error":false,"session_id":"s"}`)
		}
	})
	opts := Options{Transport: c.f, Env: []string{"CLAUDE_CODE_PRINT_BG_WAIT_CEILING_MS=150"},
		CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) { return Allow{}, nil }}
	go func() {
		for range Query(ctxT(t), "hi", opts) {
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for !contains2(c.f.Events(), "close_input") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !contains2(c.f.Events(), "close_input") {
		t.Fatal("input was never closed without an idle frame")
	}
	c.f.EOF(transport.ExitStatus{})
}

var _ = json.Valid
var _ = protocol.Decode

// Reinitialize while the callback is still running: the CLI lists the request
// as pending, the listing must not start a second callback, and the answer
// goes out when the first one completes.
func TestPendingRequestStillInflightIsAnsweredOnCompletion(t *testing.T) {
	c := newFakeCLI(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	cl := newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) {
		calls.Add(1)
		close(started)
		<-release
		return Allow{}, nil
	}})
	body := `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"toolu_1"}`
	c.request("slow-1", body)
	<-started
	c.f.OnWrite(func(line []byte) {
		if s := string(line); strings.Contains(s, `"initialize"`) {
			c.f.Feed(`{"type":"control_response","response":{"subtype":"success","request_id":"` + reqID(s) + `","response":` + defaultInitResponse + `},"pending_permission_requests":[{"type":"control_request","request_id":"slow-1","request":` + body + `}]}`)
		}
	})
	if _, err := cl.Reinitialize(ctxT(t)); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitResponse(t, c.f, "slow-1")
	time.Sleep(100 * time.Millisecond)
	n := 0
	for _, w := range c.f.Writes() {
		if strings.Contains(string(w), `"request_id":"slow-1"`) && strings.Contains(string(w), "control_response") {
			n++
		}
	}
	if n != 1 || calls.Load() != 1 {
		t.Errorf("answers=%d callbacks=%d, want one of each", n, calls.Load())
	}
}

// While detaching, even the resend of a cached answer for a pending request
// must stay silent.
func TestPendingResendHonoursDetachSuppression(t *testing.T) {
	c := newFakeCLI(t)
	cl := newClientT(t, c, Options{CanUseTool: func(context.Context, ToolPermissionRequest) (PermissionDecision, error) { return Allow{}, nil }})
	body := `{"subtype":"can_use_tool","tool_name":"Bash","input":{},"tool_use_id":"toolu_1"}`
	c.request("p-1", body)
	awaitResponse(t, c.f, "p-1")
	cl.suppress.Store(true)
	before := len(c.f.Writes())
	cl.dispatch(&protocol.ControlRequest{RequestID: "p-1", Subtype: "can_use_tool", Request: json.RawMessage(body)}, true)
	time.Sleep(50 * time.Millisecond)
	if len(c.f.Writes()) != before {
		t.Errorf("a pending resend was written while suppressed: %q", c.f.Writes()[before:])
	}
	cl.suppress.Store(false)
}

func TestProcessErrorMasksMoreSecretForms(t *testing.T) {
	for _, line := range []string{`token: abc123secret`, `{"token":"abc123secret"}`, `api_key=abc123secret`, `API-KEY: abc123secret`, `authorization: Basic abc123secret`, `x-api-key: abc123secret`} {
		msg := (&ProcessError{ExitCode: 1, StderrTail: "boom\n" + line}).Error()
		if strings.Contains(msg, "abc123secret") {
			t.Errorf("%q leaked: %s", line, msg)
		}
	}
}
