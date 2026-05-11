package claude

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/albertocavalcante/claude-agent-sdk-go/internal/transport"
)

// hookErrorSentinel is the error returned by a misbehaving test hook.
var hookErrorSentinel = errors.New("hook failure")

func TestHookErrorSurfacedOnChannel(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_1","name":"Bash","input":{"command":"ls"}}]}`),
			json.RawMessage(`{"type":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":"out"}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_hookerr"}`),
		},
	}

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					return HookOutput{}, hookErrorSentinel
				},
			},
		},
	}, mock)

	ctx := context.Background()
	var hookErrs []*HookError
	var sawNonHookErr error
	for moe := range client.Query(ctx, "test") {
		if moe.Err != nil {
			var he *HookError
			if errors.As(moe.Err, &he) {
				hookErrs = append(hookErrs, he)
				continue
			}
			sawNonHookErr = moe.Err
		}
	}

	if sawNonHookErr != nil {
		t.Fatalf("unexpected non-hook error: %v", sawNonHookErr)
	}
	if len(hookErrs) != 1 {
		t.Fatalf("expected 1 HookError on channel, got %d", len(hookErrs))
	}
	he := hookErrs[0]
	if he.Event != HookPreToolUse {
		t.Errorf("expected Event=%q, got %q", HookPreToolUse, he.Event)
	}
	if he.ToolName != "Bash" {
		t.Errorf("expected ToolName=%q, got %q", "Bash", he.ToolName)
	}
	if !errors.Is(he, hookErrorSentinel) {
		t.Errorf("expected errors.Is(he, hookErrorSentinel) == true")
	}
	if he.Error() == "" {
		t.Error("expected non-empty error string")
	}
}

func TestHookErrorDoesNotShortCircuitSubsequentHooks(t *testing.T) {
	mock := &transport.MockTransport{
		RawLines: []json.RawMessage{
			json.RawMessage(`{"type":"assistant","content":[{"type":"tool_use","id":"tu_2","name":"Bash","input":{}}]}`),
			json.RawMessage(`{"type":"result","is_error":false,"session_id":"sess_hookerr2"}`),
		},
	}

	var mu sync.Mutex
	var secondCalled bool

	client := newClientWithTransport(Options{
		Hooks: []HookRegistration{
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					return HookOutput{}, hookErrorSentinel
				},
			},
			{
				Event: HookPreToolUse,
				Callback: func(ctx context.Context, event HookInput) (HookOutput, error) {
					mu.Lock()
					secondCalled = true
					mu.Unlock()
					return HookOutput{}, nil
				},
			},
		},
	}, mock)

	ctx := context.Background()
	for range client.Query(ctx, "test") {
	}

	mu.Lock()
	defer mu.Unlock()
	if !secondCalled {
		t.Error("expected second hook to still be invoked after first hook errored")
	}
}

func TestHookErrorIsHelper(t *testing.T) {
	he := &HookError{Event: HookPostToolUse, ToolName: "Read", Err: hookErrorSentinel}
	if !IsHookError(he) {
		t.Error("expected IsHookError to return true")
	}
	if IsHookError(errors.New("not a hook error")) {
		t.Error("expected IsHookError to return false for plain error")
	}
}
