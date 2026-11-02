package protocol

import (
	"encoding/json"
	"testing"
)

func jsonEq(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got invalid JSON %q: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("\n got %s\nwant %s", gb, wb)
	}
}

func TestEncodeUserInput(t *testing.T) {
	b, err := EncodeUser(UserInput{Content: TextContent("hi"), UUID: "u-1"})
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, b, `{"type":"user","message":{"role":"user","content":"hi"},"parent_tool_use_id":null,"session_id":"","uuid":"u-1","origin":{"kind":"human"}}`)

	b, _ = EncodeUser(UserInput{Content: BlocksContent(&TextBlock{Text: "a"}, &ImageBlock{Source: json.RawMessage(`{"type":"base64","media_type":"image/png","data":"AA"}`)}), UUID: "u"})
	jsonEq(t, b, `{"type":"user","message":{"role":"user","content":[{"type":"text","text":"a"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AA"}}]},"parent_tool_use_id":null,"session_id":"","uuid":"u","origin":{"kind":"human"}}`)
}

func TestEncodeControlRequest(t *testing.T) {
	cases := []struct {
		p    RequestPayload
		want string
	}{
		{&InitializeRequest{}, `{"subtype":"initialize","hooks":null}`},
		{&InitializeRequest{Hooks: map[string][]HookRegistration{"PreToolUse": {{Matcher: "Bash", HookCallbackIDs: []string{"hook_0"}}}}},
			`{"subtype":"initialize","hooks":{"PreToolUse":[{"matcher":"Bash","hookCallbackIds":["hook_0"]}]}}`},
		{&InterruptRequest{}, `{"subtype":"interrupt"}`},
		{&SetPermissionModeRequest{Mode: "plan"}, `{"subtype":"set_permission_mode","mode":"plan"}`},
		{&SetModelRequest{Model: "haiku"}, `{"subtype":"set_model","model":"haiku"}`},
		{&SetModelRequest{}, `{"subtype":"set_model","model":null}`},
		{&MCPStatusRequest{}, `{"subtype":"mcp_status"}`},
		{&ContextUsageRequest{}, `{"subtype":"get_context_usage"}`},
		{&RewindFilesRequest{UserMessageID: "m", DryRun: true}, `{"subtype":"rewind_files","user_message_id":"m","dry_run":true}`},
		{&StopTaskRequest{TaskID: "t"}, `{"subtype":"stop_task","task_id":"t"}`},
	}
	for _, c := range cases {
		b, err := EncodeControlRequest("req_1", c.p)
		if err != nil {
			t.Fatal(err)
		}
		jsonEq(t, b, `{"type":"control_request","request_id":"req_1","request":`+c.want+`}`)
	}
}

func TestEncodeGenericRequest(t *testing.T) {
	b, err := EncodeRawControlRequest("r", "list_models", nil)
	if err != nil {
		t.Fatal(err)
	}
	jsonEq(t, b, `{"type":"control_request","request_id":"r","request":{"subtype":"list_models"}}`)
	b, _ = EncodeRawControlRequest("r", "mcp_reconnect", map[string]any{"serverName": "s"})
	jsonEq(t, b, `{"type":"control_request","request_id":"r","request":{"subtype":"mcp_reconnect","serverName":"s"}}`)
	if _, err := EncodeRawControlRequest("r", "x", 5); err == nil {
		t.Error("non-object payload must be rejected")
	}
}

func TestEncodeResponses(t *testing.T) {
	b, _ := EncodeControlSuccess("c1", AllowResponse{UpdatedInput: json.RawMessage(`{"a":1}`), ToolUseID: "toolu_1",
		UpdatedPermissions: []PermissionUpdate{{Type: PermSetMode, Mode: "acceptEdits", Destination: "session"}}})
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"success","request_id":"c1","response":{"behavior":"allow","updatedInput":{"a":1},"toolUseID":"toolu_1","updatedPermissions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}}}`)
	b, _ = EncodeControlSuccess("c2", DenyResponse{Message: "no", Interrupt: true, ToolUseID: "t"})
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"success","request_id":"c2","response":{"behavior":"deny","message":"no","interrupt":true,"toolUseID":"t"}}}`)
	b, _ = EncodeControlSuccess("c3", DenyResponse{Message: "no"})
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"success","request_id":"c3","response":{"behavior":"deny","message":"no"}}}`)
	b, _ = EncodeControlError("c4", "Unsupported control request subtype: x")
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"error","request_id":"c4","error":"Unsupported control request subtype: x"}}`)

	tr := true
	b, _ = EncodeControlSuccess("c5", HookOutput{Continue: &tr, HookSpecificOutput: json.RawMessage(`{"hookEventName":"PreToolUse","permissionDecision":"ask"}`)})
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"success","request_id":"c5","response":{"continue":true,"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask"}}}}`)
	b, _ = EncodeControlSuccess("c6", HookOutput{Async: true, AsyncTimeout: 500})
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"success","request_id":"c6","response":{"async":true,"asyncTimeout":500}}}`)
	b, _ = EncodeControlSuccess("c7", ElicitationResult{Action: "decline"})
	jsonEq(t, b, `{"type":"control_response","response":{"subtype":"success","request_id":"c7","response":{"action":"decline"}}}`)
}

func TestPermissionUpdateRoundTrip(t *testing.T) {
	in := `[{"type":"addRules","rules":[{"toolName":"Bash","ruleContent":"git *"}],"behavior":"allow","destination":"userSettings"},{"type":"addDirectories","directories":["/p"],"destination":"session"},{"type":"setMode","mode":"plan","destination":"session"}]`
	var ups []PermissionUpdate
	if err := json.Unmarshal([]byte(in), &ups); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(ups)
	jsonEq(t, out, in)
}

func TestParseResponses(t *testing.T) {
	ir, err := ParseInitializeResponse(json.RawMessage(`{"commands":[{"name":"x"}],"models":[{"value":"m"}],"account":{"k":"v"},"pid":42,"claude_code_version":"2.1.316","current_permission_mode":"default","capabilities":["a"],"session_state":"idle","new":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if ir.PID != 42 || ir.ClaudeCodeVersion != "2.1.316" || ir.CurrentPermissionMode != "default" || ir.Capabilities[0] != "a" || ir.SessionState != "idle" {
		t.Errorf("%+v", ir)
	}
	if string(ir.Commands) != `[{"name":"x"}]` || string(ir.Models) == "" || string(ir.Account) != `{"k":"v"}` || len(ir.Raw) == 0 {
		t.Errorf("raw fields %+v", ir)
	}
	rr, err := ParseInterruptResponse(json.RawMessage(`{"still_queued":["a"]}`))
	if err != nil || len(rr.StillQueued) != 1 {
		t.Errorf("%+v %v", rr, err)
	}
	mr, err := ParseMCPStatusResponse(json.RawMessage(`{"mcpServers":[{"name":"s","status":"connected","scope":"claudeai"}]}`))
	if err != nil || mr.MCPServers[0].Status != "connected" {
		t.Errorf("%+v %v", mr, err)
	}
	rw, err := ParseRewindFilesResponse(json.RawMessage(`{"canRewind":true,"filesChanged":["a"],"insertions":1,"deletions":2}`))
	if err != nil || !rw.CanRewind || rw.Insertions != 1 {
		t.Errorf("%+v %v", rw, err)
	}
}
