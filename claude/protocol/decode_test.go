package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustDecode(t *testing.T, line string) Frame {
	t.Helper()
	f, err := Decode([]byte(line), Limits{})
	if err != nil {
		t.Fatalf("Decode(%s): %v", line, err)
	}
	return f
}

func TestDecodeAssistant(t *testing.T) {
	line := `{"type":"assistant","uuid":"u1","session_id":"s1","parent_tool_use_id":"toolu_1","future":1,"message":{"id":"msg_1","model":"m","role":"assistant","stop_reason":null,"content":[{"type":"text","text":"hi"},{"type":"thinking","thinking":"","signature":"sig"},{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"ls"},"caller":{"type":"direct"}},{"type":"mystery","x":1}],"usage":{"input_tokens":3}}}`
	m, ok := mustDecode(t, line).(*Assistant)
	if !ok {
		t.Fatal("not *Assistant")
	}
	if m.Message.ID != "msg_1" || m.UUID != "u1" || m.SessionID != "s1" || m.ParentToolUseID != "toolu_1" {
		t.Errorf("fields: %+v", m)
	}
	if len(m.Message.Content) != 4 {
		t.Fatalf("blocks = %d", len(m.Message.Content))
	}
	if tb := m.Message.Content[0].(*TextBlock); tb.Text != "hi" || len(tb.Raw) == 0 {
		t.Errorf("text block %+v", tb)
	}
	if th := m.Message.Content[1].(*ThinkingBlock); th.Signature != "sig" {
		t.Errorf("thinking %+v", th)
	}
	tu := m.Message.Content[2].(*ToolUseBlock)
	if tu.ID != "toolu_2" || tu.Name != "Bash" || string(tu.Input) != `{"command":"ls"}` {
		t.Errorf("tool_use %+v", tu)
	}
	ub := m.Message.Content[3].(*UnknownBlock)
	if ub.Type != "mystery" || string(ub.Raw) != `{"type":"mystery","x":1}` {
		t.Errorf("unknown block %+v", ub)
	}
	if string(m.Raw) != line {
		t.Error("Raw not preserved")
	}
	if text := m.Text(); text != "hi" {
		t.Errorf("Text() = %q", text)
	}
}

func TestDecodeAssistantFlat(t *testing.T) {
	m := mustDecode(t, `{"type":"assistant","model":"m","content":[{"type":"text","text":"flat"}]}`).(*Assistant)
	if m.Text() != "flat" || m.Message.Model != "m" {
		t.Errorf("flat envelope: %+v", m)
	}
}

func TestDecodeAssistantAbortedAndError(t *testing.T) {
	m := mustDecode(t, `{"type":"assistant","aborted":true,"error":"rate_limit","message":{"id":"m","content":[]}}`).(*Assistant)
	if !m.Aborted || m.Error != "rate_limit" {
		t.Errorf("%+v", m)
	}
}

func TestDecodeUserContentShapes(t *testing.T) {
	cases := []struct {
		name, line string
		check      func(t *testing.T, u *User)
	}{
		{"string", `{"type":"user","message":{"role":"user","content":"hello"},"isReplay":true,"uuid":"u","timestamp":"t"}`,
			func(t *testing.T, u *User) {
				if !u.Message.Content.IsText || u.Message.Content.Text != "hello" || !u.IsReplay || u.UUID != "u" {
					t.Errorf("%+v", u)
				}
			}},
		{"blocks", `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"a"}]}}`,
			func(t *testing.T, u *User) {
				if u.Message.Content.IsText || len(u.Message.Content.Blocks) != 1 {
					t.Errorf("%+v", u)
				}
			}},
		{"tool_result string", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out","is_error":true}]},"tool_use_result":{"stdout":"out"}}`,
			func(t *testing.T, u *User) {
				tr := u.Message.Content.Blocks[0].(*ToolResultBlock)
				if tr.ToolUseID != "t1" || !tr.IsError || !tr.Content.IsText || tr.Content.Text != "out" {
					t.Errorf("%+v", tr)
				}
				if string(u.ToolUseResult) != `{"stdout":"out"}` {
					t.Errorf("tool_use_result %s", u.ToolUseResult)
				}
			}},
		{"tool_result blocks", `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"text","text":"x"},{"type":"tool_reference","tool_name":"n"}]}]}}`,
			func(t *testing.T, u *User) {
				tr := u.Message.Content.Blocks[0].(*ToolResultBlock)
				if tr.Content.IsText || len(tr.Content.Blocks) != 2 {
					t.Fatalf("%+v", tr)
				}
				if _, ok := tr.Content.Blocks[1].(*UnknownBlock); !ok {
					t.Errorf("tool_reference should be UnknownBlock, got %T", tr.Content.Blocks[1])
				}
				if tr.Content.String() != "x" {
					t.Errorf("String() = %q", tr.Content.String())
				}
			}},
		{"flat", `{"type":"user","content":"flat text"}`,
			func(t *testing.T, u *User) {
				if u.Message.Content.Text != "flat text" {
					t.Errorf("%+v", u)
				}
			}},
		{"null content", `{"type":"user","message":{"role":"user","content":null}}`,
			func(t *testing.T, u *User) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u, ok := mustDecode(t, c.line).(*User)
			if !ok {
				t.Fatal("not *User")
			}
			if string(u.Raw) != c.line {
				t.Error("Raw not preserved")
			}
			c.check(t, u)
		})
	}
}

func TestDecodeSystem(t *testing.T) {
	init := mustDecode(t, `{"type":"system","subtype":"init","session_id":"s","uuid":"u","cwd":"/work","tools":["Bash"],"mcp_servers":[{"name":"a","status":"pending"}],"model":"claude-x","permissionMode":"plan","capabilities":["c1"],"claude_code_version":"2.1.316","new_field":true}`).(*SystemInit)
	if init.SessionID != "s" || init.Model != "claude-x" || init.PermissionMode != "plan" || init.Tools[0] != "Bash" ||
		init.MCPServers[0].Name != "a" || init.Capabilities[0] != "c1" || init.CWD != "/work" || init.ClaudeCodeVersion != "2.1.316" {
		t.Errorf("%+v", init)
	}
	if init.Subtype() != "init" {
		t.Error("subtype")
	}
	st := mustDecode(t, `{"type":"system","subtype":"status","status":null,"permissionMode":"acceptEdits"}`).(*SystemStatus)
	if st.Status != "" || st.PermissionMode != "acceptEdits" {
		t.Errorf("%+v", st)
	}
	st = mustDecode(t, `{"type":"system","subtype":"status","status":"compacting"}`).(*SystemStatus)
	if st.Status != "compacting" {
		t.Errorf("%+v", st)
	}
	cb := mustDecode(t, `{"type":"system","subtype":"compact_boundary","compact_metadata":{"trigger":"manual","pre_tokens":10,"post_tokens":3}}`).(*CompactBoundary)
	if cb.Trigger != "manual" || cb.PreTokens != 10 || cb.PostTokens != 3 {
		t.Errorf("%+v", cb)
	}
	ss := mustDecode(t, `{"type":"system","subtype":"session_state_changed","state":"idle","sdk_host_only":true}`).(*SessionStateChanged)
	if ss.State != "idle" || !ss.SDKHostOnly {
		t.Errorf("%+v", ss)
	}
	su := mustDecode(t, `{"type":"system","subtype":"task_started","task_id":"x"}`).(*SystemUnknown)
	if su.SubtypeName != "task_started" || !bytes.Contains(su.Raw, []byte("task_id")) {
		t.Errorf("%+v", su)
	}
}

func TestDecodeResult(t *testing.T) {
	line := `{"duration_api_ms":5,"type":"result","subtype":"error_during_execution","is_error":true,"duration_ms":9,"num_turns":2,"result":"r","stop_reason":null,"session_id":"s","uuid":"u","total_cost_usd":0.008,"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4},"modelUsage":{"claude-haiku-5-5":{"inputTokens":7,"outputTokens":8,"costUSD":0.5,"contextWindow":200000}},"model_usage_since_last_result":{"claude-haiku-5-5":{"inputTokens":1}},"terminal_reason":"aborted_streaming","errors":["e"],"queued_turn_count":1,"result_index":3,"permission_denials":[{"tool_name":"Bash"}]}`
	r := mustDecode(t, line).(*Result)
	if r.Subtype != "error_during_execution" || !r.IsError || r.TerminalReason != "aborted_streaming" || r.NumTurns != 2 ||
		r.TotalCostUSD != 0.008 || r.Usage.InputTokens != 1 || r.Usage.CacheReadInputTokens != 3 || r.SessionID != "s" {
		t.Errorf("%+v", r)
	}
	if mu := r.ModelUsage["claude-haiku-5-5"]; mu.InputTokens != 7 || mu.CostUSD != 0.5 || mu.ContextWindow != 200000 {
		t.Errorf("modelUsage %+v", mu)
	}
	if r.ModelUsageSinceLastResult["claude-haiku-5-5"].InputTokens != 1 {
		t.Error("since last result")
	}
	if len(r.Errors) != 1 || r.QueuedTurnCount != 1 || r.ResultIndex != 3 || len(r.PermissionDenials) == 0 {
		t.Errorf("%+v", r)
	}
	if string(r.Raw) != line {
		t.Error("Raw")
	}
}

func TestDecodeStreamEventAndOthers(t *testing.T) {
	se := mustDecode(t, `{"type":"stream_event","uuid":"u","session_id":"s","parent_tool_use_id":null,"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ab"}}}`).(*StreamEvent)
	if se.EventType != "content_block_delta" {
		t.Errorf("event type %q", se.EventType)
	}
	if d, ok := se.TextDelta(); !ok || d != "ab" {
		t.Errorf("TextDelta = %q %v", d, ok)
	}
	rl := mustDecode(t, `{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","rateLimitType":"five_hour","resetsAt":1}}`).(*RateLimitEvent)
	if rl.Status != "allowed" || rl.RateLimitType != "five_hour" {
		t.Errorf("%+v", rl)
	}
	cr := mustDecode(t, `{"type":"conversation_reset","new_conversation_id":"n"}`).(*ConversationReset)
	if cr.NewConversationID != "n" {
		t.Errorf("%+v", cr)
	}
	un := mustDecode(t, `{"type":"tool_progress","a":1}`).(*Unknown)
	if un.Type != "tool_progress" || string(un.Raw) != `{"type":"tool_progress","a":1}` {
		t.Errorf("%+v", un)
	}
	un = mustDecode(t, `{"no_type":1}`).(*Unknown)
	if un.Type != "" {
		t.Errorf("%+v", un)
	}
	if _, ok := mustDecode(t, `{"type":"keep_alive"}`).(*KeepAlive); !ok {
		t.Error("keep_alive")
	}
}

func TestDecodeControlFrames(t *testing.T) {
	req := mustDecode(t, `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"command":"ls"},"tool_use_id":"toolu_1","permission_suggestions":[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"ls"}],"behavior":"allow","destination":"session"}],"decision_reason_type":"hook","agent_id":"a"}}`).(*ControlRequest)
	if req.RequestID != "r1" || req.Subtype != "can_use_tool" {
		t.Errorf("%+v", req)
	}
	p, err := req.Payload()
	if err != nil {
		t.Fatal(err)
	}
	cu := p.(*CanUseToolRequest)
	if cu.ToolName != "Bash" || cu.ToolUseID != "toolu_1" || string(cu.Input) != `{"command":"ls"}` || cu.AgentID != "a" ||
		len(cu.PermissionSuggestions) != 1 || cu.PermissionSuggestions[0].Rules[0].RuleContent != "ls" {
		t.Errorf("%+v", cu)
	}

	hk, _ := mustDecode(t, `{"type":"control_request","request_id":"r2","request":{"subtype":"hook_callback","callback_id":"hook_0","input":{"hook_event_name":"PreToolUse"},"tool_use_id":null}}`).(*ControlRequest).Payload()
	if h := hk.(*HookCallbackRequest); h.CallbackID != "hook_0" || h.ToolUseID != "" {
		t.Errorf("%+v", h)
	}
	el, _ := mustDecode(t, `{"type":"control_request","request_id":"r3","request":{"subtype":"elicitation","mcp_server_name":"srv","message":"m","mode":"form","requested_schema":{"type":"object"}}}`).(*ControlRequest).Payload()
	if e := el.(*ElicitationRequest); e.MCPServerName != "srv" || e.Message != "m" || string(e.RequestedSchema) != `{"type":"object"}` {
		t.Errorf("%+v", e)
	}
	unk, err := mustDecode(t, `{"type":"control_request","request_id":"r4","request":{"subtype":"bogus"}}`).(*ControlRequest).Payload()
	if err != nil || unk != nil {
		t.Errorf("unknown payload = %v, %v", unk, err)
	}

	c := mustDecode(t, `{"type":"control_cancel_request","request_id":"r1"}`).(*ControlCancelRequest)
	if c.RequestID != "r1" {
		t.Error("cancel id")
	}

	ok := mustDecode(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r9","response":{"mode":"plan"}},"pending_permission_requests":[{"type":"control_request","request_id":"p1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{}}}],"pending_user_dialog_requests":[]}`).(*ControlResponse)
	if ok.RequestID != "r9" || ok.IsError() || string(ok.Response) != `{"mode":"plan"}` {
		t.Errorf("%+v", ok)
	}
	if len(ok.PendingPermissionRequests) != 1 || ok.PendingPermissionRequests[0].RequestID != "p1" || !ok.PendingKnown {
		t.Errorf("pending: %+v", ok.PendingPermissionRequests)
	}
	bad := mustDecode(t, `{"type":"control_response","response":{"subtype":"error","request_id":"r8","error":"nope"}}`).(*ControlResponse)
	if !bad.IsError() || bad.Error != "nope" || bad.RequestID != "r8" || bad.PendingKnown {
		t.Errorf("%+v", bad)
	}
	// pending list nested in the inner response is tolerated too
	inner := mustDecode(t, `{"type":"control_response","response":{"subtype":"success","request_id":"r7","response":{},"pending_permission_requests":[{"type":"control_request","request_id":"p2","request":{"subtype":"can_use_tool","tool_name":"X","input":{}}}]}}`).(*ControlResponse)
	if len(inner.PendingPermissionRequests) != 1 {
		t.Error("inner pending list ignored")
	}
}

func TestDecodeSkipsAndLimits(t *testing.T) {
	for _, s := range []string{"", "   ", "[SandboxDebug] hello", "[1,2]", "null"} {
		if _, err := Decode([]byte(s), Limits{}); !errors.Is(err, ErrSkipLine) {
			t.Errorf("Decode(%q) err = %v, want ErrSkipLine", s, err)
		}
	}
	if _, err := Decode([]byte(`{"type":`), Limits{}); err == nil || errors.Is(err, ErrSkipLine) {
		t.Errorf("truncated object should be a decode error, got %v", err)
	}
	var de *DecodeError
	if _, err := Decode([]byte(`{"type":`), Limits{}); !errors.As(err, &de) {
		t.Errorf("want *DecodeError, got %T", err)
	}
	if _, err := Decode([]byte(`{"type":"x","pad":"`+strings.Repeat("a", 100)+`"}`), Limits{MaxLineBytes: 50}); !errors.Is(err, ErrLineTooLong) {
		t.Errorf("want ErrLineTooLong, got %v", err)
	}
	deep := `{"a":` + strings.Repeat("[", 300) + strings.Repeat("]", 300) + `}`
	if _, err := Decode([]byte(deep), Limits{}); !errors.Is(err, ErrTooDeep) {
		t.Errorf("want ErrTooDeep, got %v", err)
	}
	if _, err := Decode([]byte(`{"type":"x","a":[[[1]]]}`), Limits{MaxDepth: 4}); err != nil {
		t.Errorf("depth 4 should pass: %v", err)
	}
	if _, err := Decode([]byte(`{"type":"x","a":[[[[1]]]]}`), Limits{MaxDepth: 4}); !errors.Is(err, ErrTooDeep) {
		t.Errorf("depth 5 should fail: %v", err)
	}
	// brackets inside strings do not count
	if _, err := Decode([]byte(`{"type":"x","a":"`+strings.Repeat("[", 1000)+`\"[[[[`+`"}`), Limits{}); err != nil {
		t.Errorf("brackets in strings: %v", err)
	}
}

func TestDepthBombIsCheap(t *testing.T) {
	bomb := []byte("{\"a\":" + strings.Repeat("[", 8<<20))
	if _, err := Decode(bomb, Limits{MaxLineBytes: 16 << 20}); !errors.Is(err, ErrTooDeep) {
		t.Fatalf("err = %v", err)
	}
}

// M1/M3: a field whose type drifts never downgrades the frame or loses routing.
func TestTypeDriftNeverDowngrades(t *testing.T) {
	t.Run("result with object errors and string num_turns", func(t *testing.T) {
		r, ok := mustDecode(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"s","errors":[{"message":"boom"}],"num_turns":"2","total_cost_usd":"x","usage":5}`).(*Result)
		if !ok || !r.IsError || r.Subtype != "error_during_execution" || r.SessionID != "s" {
			t.Fatalf("%+v", r)
		}
	})
	t.Run("init with object tools", func(t *testing.T) {
		i, ok := mustDecode(t, `{"type":"system","subtype":"init","session_id":"sess-1","tools":[{"name":"Bash"}],"mcp_servers":"none","model":7}`).(*SystemInit)
		if !ok || i.SessionID != "sess-1" {
			t.Fatalf("%+v", i)
		}
	})
	t.Run("assistant with odd fields", func(t *testing.T) {
		a, ok := mustDecode(t, `{"type":"assistant","error":{"code":1},"aborted":"yes","message":{"id":"m","content":[{"type":"text","text":"hi"}],"usage":3}}`).(*Assistant)
		if !ok || a.Text() != "hi" {
			t.Fatalf("%+v", a)
		}
	})
	t.Run("can_use_tool with odd fields stays a request", func(t *testing.T) {
		req := mustDecode(t, `{"type":"control_request","request_id":"r1","request":{"subtype":"can_use_tool","tool_name":"Bash","input":{"a":1},"tool_use_id":"t","permission_suggestions":"x","blocked_path":5,"decision_reason":{"k":1}}}`).(*ControlRequest)
		p, err := req.Payload()
		cu, _ := p.(*CanUseToolRequest)
		if err != nil || cu == nil || cu.ToolName != "Bash" || string(cu.Input) != `{"a":1}` || cu.ToolUseID != "t" {
			t.Fatalf("%v %+v", err, cu)
		}
	})
	t.Run("error response with numeric or object error fields routes", func(t *testing.T) {
		c := mustDecode(t, `{"type":"control_response","response":{"subtype":"error","request_id":"r8","error":"nope","error_code":42}}`).(*ControlResponse)
		if c.RequestID != "r8" || !c.IsError() || c.Error != "nope" || c.ErrorCode != "42" {
			t.Errorf("%+v", c)
		}
		c = mustDecode(t, `{"type":"control_response","response":{"subtype":"error","request_id":"r9","error":{"message":"m"}}}`).(*ControlResponse)
		if c.RequestID != "r9" || c.Error != `{"message":"m"}` {
			t.Errorf("%+v", c)
		}
		c = mustDecode(t, `{"type":"control_response","response":{"subtype":"success","request_id":7,"response":{}}}`).(*ControlResponse)
		if c.RequestID != "7" {
			t.Errorf("numeric request id: %+v", c)
		}
	})
	t.Run("initialize response with drifted fields", func(t *testing.T) {
		ir, err := ParseInitializeResponse(json.RawMessage(`{"pid":"x","capabilities":"y","claude_code_version":"2.1.316","models":[1]}`))
		if err != nil || ir.ClaudeCodeVersion != "2.1.316" {
			t.Fatalf("%+v %v", ir, err)
		}
	})
}

func FuzzDecodeLine(f *testing.F) {
	for _, s := range []string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"x"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text"}]}]}}`,
		`{"type":"control_request","request_id":"r","request":{"subtype":"can_use_tool"}}`,
		`{"type":"control_response","response":{"subtype":"success"}}`,
		`{"type":"result","modelUsage":{"m":{}}}`,
		`{"type":"system","subtype":"init","tools":null}`,
		`[[[[[[`, `{"a":[[[[[[[[[[`, `{"type":1}`, `{"type":"user","message":5}`, "",
	} {
		f.Add([]byte(s))
	}
	lim := Limits{MaxLineBytes: 1 << 16, MaxDepth: 32}
	f.Fuzz(func(t *testing.T, data []byte) {
		fr, err := Decode(data, lim)
		if len(data) > lim.MaxLineBytes && !errors.Is(err, ErrLineTooLong) && !errors.Is(err, ErrSkipLine) {
			t.Fatalf("oversized line accepted: %v", err)
		}
		if err == nil {
			if fr == nil {
				t.Fatal("nil frame without error")
			}
			if m, ok := fr.(Message); ok && !json.Valid(m.RawJSON()) {
				t.Fatalf("Raw is not valid JSON: %q", m.RawJSON())
			}
		}
	})
}

// TestDecodeRecordedTranscripts decodes every CLI line of every recorded
// session: nothing may fail, and Raw must round-trip the line.
func TestDecodeRecordedTranscripts(t *testing.T) {
	files, _ := filepath.Glob("../testdata/transcripts/*/*.transcript.jsonl")
	if len(files) < 10 {
		t.Fatalf("expected recorded transcripts, found %d", len(files))
	}
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(nil, 32<<20)
		counts := map[string]int{}
		for sc.Scan() {
			var rec struct {
				Dir string          `json:"dir"`
				Msg json.RawMessage `json:"msg"`
			}
			if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
				t.Fatalf("%s: %v", f, err)
			}
			if rec.Dir != "out" {
				continue
			}
			fr, err := Decode(rec.Msg, Limits{})
			if err != nil {
				t.Errorf("%s: %v", f, err)
				continue
			}
			counts[frameName(fr)]++
			if m, ok := fr.(Message); ok && !bytes.Equal(m.RawJSON(), rec.Msg) {
				t.Errorf("%s: Raw mismatch for %s", f, frameName(fr))
			}
		}
		_ = fh.Close()
		if counts["*protocol.Unknown"] > 0 {
			t.Logf("%s: unknown frames: %d", filepath.Base(f), counts["*protocol.Unknown"])
		}
		if counts["*protocol.Result"] == 0 && !strings.Contains(f, "approval-parent-kill") && !strings.Contains(f, "control.") && !strings.Contains(f, "hostile") {
			t.Errorf("%s: no result frame decoded: %v", f, counts)
		}
	}
}

func frameName(f Frame) string {
	return fmt.Sprintf("%T", f)
}
