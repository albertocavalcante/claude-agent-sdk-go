package claude

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/albertocavalcante/claude-agent-sdk-go/internal/transport"
)

func TestParseNestedCLIEnvelope(t *testing.T) {
	t.Run("assistant", func(t *testing.T) {
		m, err := ParseMessage([]byte(`{"type":"assistant","message":{"model":"claude-sonnet-4-6","stop_reason":"end_turn","content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"go.mod"}}]}}`))
		if err != nil {
			t.Fatal(err)
		}
		am := m.(*AssistantMessage)
		if am.Model != "claude-sonnet-4-6" || am.StopReason != "end_turn" || len(am.Content) != 2 {
			t.Errorf("real CLI envelope lost response/tool content: %#v", am)
		}
	})
	t.Run("user_tool_result", func(t *testing.T) {
		m, err := ParseMessage([]byte(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"module example"}]}}`))
		if err != nil {
			t.Fatal(err)
		}
		um := m.(*UserMessage)
		if len(um.Content) != 1 {
			t.Errorf("real CLI envelope lost tool result: %#v", um)
		}
	})
}
func TestParseCLIResultUsage(t *testing.T) {
	m, err := ParseMessage([]byte(`{"type":"result","subtype":"success","is_error":false,"duration_ms":20,"duration_api_ms":10,"num_turns":1,"session_id":"s1","total_cost_usd":0.0123,"usage":{"input_tokens":42,"output_tokens":7}}`))
	if err != nil {
		t.Fatal(err)
	}
	rm := m.(*ResultMessage)
	if rm.Cost != 0.0123 || rm.InputTokens != 42 || rm.OutputTokens != 7 {
		t.Errorf("real CLI usage silently zeroed: %#v", rm)
	}
}
func TestOneShotQueryRunsHooks(t *testing.T) {
	count := 0
	opts := Options{Hooks: []HookRegistration{{Event: HookMessage, Callback: func(context.Context, HookInput) (HookOutput, error) { count++; return HookOutput{}, nil }}}}
	mock := &transport.MockTransport{RawLines: []json.RawMessage{json.RawMessage(`{"type":"system","subtype":"init"}`), json.RawMessage(`{"type":"result"}`)}}
	for moe := range queryWithTransport(context.Background(), "hello", opts, mock) {
		if moe.Err != nil {
			t.Fatal(moe.Err)
		}
	}
	if count != 2 {
		t.Errorf("one-shot Query ran %d hooks, want 2", count)
	}
}

func TestParseNestedUserText(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"type":"user","message":{"role":"user","content":"hello"}}`))
	if err != nil {
		t.Fatal(err)
	}
	user := msg.(*UserMessage)
	if len(user.Content) != 1 {
		t.Fatalf("missing user text: %#v", user)
	}
	text, ok := user.Content[0].(*TextBlock)
	if !ok || text.Text != "hello" {
		t.Fatalf("wrong user text: %#v", user.Content[0])
	}
}
func TestParseMalformedNestedMessage(t *testing.T) {
	for _, data := range []string{
		`{"type":"assistant","message":"invalid"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":123}]}}`,
		`{"type":"user","message":{"content":123}}`,
		`{"type":"result","usage":{"input_tokens":"invalid"}}`,
	} {
		t.Run(data, func(t *testing.T) {
			_, err := ParseMessage([]byte(data))
			var protocolErr *ProtocolError
			if !errors.As(err, &protocolErr) {
				t.Fatalf("expected protocol error, got %v", err)
			}
		})
	}
}
func TestParseNestedFieldsOverrideLegacyFields(t *testing.T) {
	msg, err := ParseMessage([]byte(`{"type":"assistant","model":"legacy","content":[{"type":"text","text":"legacy"}],"message":{"model":"","content":[]}}`))
	if err != nil {
		t.Fatal(err)
	}
	assistant := msg.(*AssistantMessage)
	if assistant.Model != "" || len(assistant.Content) != 0 {
		t.Fatalf("legacy fields overrode nested message: %#v", assistant)
	}
	msg, err = ParseMessage([]byte(`{"type":"result","cost_usd":1,"input_tokens":42,"output_tokens":7,"total_cost_usd":0,"usage":{"input_tokens":0,"output_tokens":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	result := msg.(*ResultMessage)
	if result.Cost != 0 || result.InputTokens != 0 || result.OutputTokens != 0 {
		t.Fatalf("legacy fields overrode CLI usage: %#v", result)
	}
}
func TestQueryHooksExactlyOnceAndErrorOrder(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		name := "one-shot"
		if persistent {
			name = "client"
		}
		t.Run(name, func(t *testing.T) {
			calls := map[HookEvent]int{}
			hookFailure := errors.New("hook failure")
			opts := Options{SessionID: "s1"}
			for _, event := range []HookEvent{HookMessage, HookPreToolUse, HookPostToolUse, HookResult} {
				opts.Hooks = append(opts.Hooks, HookRegistration{Event: event, Callback: func(_ context.Context, input HookInput) (HookOutput, error) {
					calls[input.Event]++
					if input.Event == HookPreToolUse {
						if input.ToolName != "Read" {
							t.Errorf("wrong pre-tool name: %q", input.ToolName)
						}
						return HookOutput{}, hookFailure
					}
					if input.Event == HookPostToolUse && (input.ToolName != "Read" || input.ToolOutput != "module example") {
						t.Errorf("wrong post-tool input: %#v", input)
					}
					if input.Event == HookResult && input.SessionID != "s2" {
						t.Errorf("wrong result session ID: %q", input.SessionID)
					}
					return HookOutput{}, nil
				}})
			}
			mock := &transport.MockTransport{RawLines: []json.RawMessage{
				json.RawMessage(`{"type":"system","subtype":"init","session_id":"s1"}`),
				json.RawMessage(`{"type":"assistant","message":{"model":"test","content":[{"type":"tool_use","id":"t1","name":"Read","input":{}}]}}`),
				json.RawMessage(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"module example"}]}}`),
				json.RawMessage(`{"type":"result","session_id":"s2"}`),
			}}
			var ch <-chan MessageOrError
			if persistent {
				client := newClientWithTransport(opts, mock)
				defer client.Close()
				ch = client.Query(context.Background(), "")
			} else {
				ch = queryWithTransport(context.Background(), "", opts, mock)
			}
			var order []string
			for item := range ch {
				if item.Err != nil {
					if !IsHookError(item.Err) || !errors.Is(item.Err, hookFailure) {
						t.Fatalf("wrong hook error: %v", item.Err)
					}
					order = append(order, "hook-error")
					continue
				}
				order = append(order, item.Message.Type())
			}
			want := []string{"system", "assistant", "hook-error", "user", "result"}
			if len(order) != len(want) {
				t.Fatalf("wrong stream order: %v", order)
			}
			for i, expected := range want {
				if order[i] != expected {
					t.Errorf("wrong stream order: %v", order)
				}
			}
			for event, wantCount := range map[HookEvent]int{HookMessage: 4, HookPreToolUse: 1, HookPostToolUse: 1, HookResult: 1} {
				if calls[event] != wantCount {
					t.Errorf("%s fired %d times, want %d", event, calls[event], wantCount)
				}
			}
		})
	}
}
