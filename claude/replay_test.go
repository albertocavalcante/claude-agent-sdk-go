package claude

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport/transporttest"
)

// Golden replay. Each transcript under testdata/transcripts is a real CLI
// session: "out" frames are what the CLI wrote, "in" frames what the
// recording driver wrote. The test feeds the out frames through the fake
// transport and drives a Client the way the driver acted (one SDK call per
// recorded in-frame); the SDK must write the recorded in-frames back, modulo
// request ids, uuids and the origin stamp. Decisions for can_use_tool and
// hook_callback are taken from the recorded answers.

type frame struct {
	Dir string          `json:"dir"`
	Msg json.RawMessage `json:"msg"`
}

func loadFrames(t *testing.T, path string) []frame {
	t.Helper()
	fh, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	var out []frame
	sc := bufio.NewScanner(fh)
	sc.Buffer(nil, 32<<20)
	for sc.Scan() {
		var f frame
		if err := json.Unmarshal(sc.Bytes(), &f); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		out = append(out, f)
	}
	return out
}

func parse(b json.RawMessage) map[string]any {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func sub(m map[string]any, keys ...string) any {
	var cur any = m
	for _, k := range keys {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = mm[k]
	}
	return cur
}

// normalize reduces an SDK-to-CLI frame to what must match the recording.
func normalize(b []byte) string {
	var v any
	_ = json.Unmarshal(b, &v)
	var walk func(any) any
	walk = func(x any) any {
		switch y := x.(type) {
		case map[string]any:
			for k, c := range y {
				if c == nil || k == "uuid" || k == "origin" || k == "toolUseID" {
					delete(y, k)
					continue
				}
				y[k] = walk(c)
			}
			if y["type"] == "control_request" {
				y["request_id"] = "<req>"
			}
			return y
		case []any:
			for i := range y {
				y[i] = walk(y[i])
			}
		}
		return x
	}
	out, _ := json.Marshal(walk(v))
	return string(out)
}

// flagMap turns argv into flag -> value, ignoring the order and the
// "--flag=value" vs "--flag value" spelling.
func flagMap(args []string) map[string]string {
	m := map[string]string{}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			continue
		}
		if k, v, ok := strings.Cut(a, "="); ok {
			m[k] = v
			continue
		}
		if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") || (i+1 < len(args) && args[i+1] == "") {
			m[a] = args[i+1]
			i++
		} else {
			m[a] = ""
		}
	}
	return m
}

// optionsFromArgv derives Options that should reproduce the recorded argv.
func optionsFromArgv(argv []string) Options {
	fm := flagMap(argv)
	var o Options
	o.Model = fm["--model"]
	o.SystemPrompt = fm["--system-prompt"]
	if v, ok := fm["--max-turns"]; ok {
		o.MaxTurns = atoi(v)
	}
	_, o.IncludePartialMessages = fm["--include-partial-messages"]
	_, replay := fm["--replay-user-messages"]
	o.ReplayUserMessages = &replay
	if v, ok := fm["--setting-sources"]; ok {
		o.SettingSources = []config.SettingSource{}
		for _, s := range strings.Split(v, ",") {
			if s != "" {
				o.SettingSources = append(o.SettingSources, config.SettingSource(s))
			}
		}
	} else {
		// the hostile "default" runs: the SDK would pass user; do not compare
		o.SettingSources = nil
	}
	if v, ok := fm["--tools"]; ok {
		o.Tools = []string{}
		if v != "" {
			o.Tools = strings.Split(v, ",")
		}
	}
	o.Resume = fm["--resume"]
	_, o.ForkSession = fm["--fork-session"]
	known := map[string]bool{"--output-format": true, "--verbose": true, "--input-format": true, "--model": true,
		"--system-prompt": true, "--max-turns": true, "--include-partial-messages": true, "--replay-user-messages": true,
		"--setting-sources": true, "--tools": true, "--resume": true, "--fork-session": true, "--permission-prompt-tool": true}
	for k, v := range fm {
		if !known[k] {
			if o.ExtraArgs == nil {
				o.ExtraArgs = map[string]string{}
			}
			o.ExtraArgs[strings.TrimPrefix(k, "--")] = v
		}
	}
	return o
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

type replayCase struct {
	file   string
	mutate func([]frame) []frame
	// extra assertions
	check func(t *testing.T, r *replayResult)
}

type replayResult struct {
	frames    []frame
	client    *Client
	fake      *transporttest.Fake
	messages  []protocol.Message
	streamErr []error
	endErr    error
	actionErr []error
	argv      []string
	events    []string
}

func transcriptPath(rel string) string { return filepath.Join("testdata", "transcripts", rel) }

func runReplay(t *testing.T, rc replayCase) *replayResult {
	t.Helper()
	frames := loadFrames(t, transcriptPath(rc.file))
	if rc.mutate != nil {
		frames = rc.mutate(frames)
	}
	res := &replayResult{frames: frames}

	// derive options from the recorded argv and the recorded answers
	var argvAny []any
	answers := map[string]json.RawMessage{} // CLI request id -> recorded response payload
	var initHooks map[string][]struct {
		Matcher string   `json:"matcher"`
		IDs     []string `json:"hookCallbackIds"`
	}
	exitCode, haveExit := 0, false
	for _, f := range frames {
		m := parse(f.Msg)
		switch f.Dir {
		case "meta":
			if a, ok := m["argv"].([]any); ok && argvAny == nil {
				argvAny = a
			}
			if c, ok := m["exit_code"].(float64); ok {
				exitCode, haveExit = int(c), true
			}
		case "in":
			switch m["type"] {
			case "control_response":
				id, _ := sub(m, "response", "request_id").(string)
				pl, _ := json.Marshal(sub(m, "response", "response"))
				answers[id] = pl
			case "control_request":
				if sub(m, "request", "subtype") == "initialize" {
					var ir struct {
						Request struct {
							Hooks map[string][]struct {
								Matcher string   `json:"matcher"`
								IDs     []string `json:"hookCallbackIds"`
							} `json:"hooks"`
						} `json:"request"`
					}
					_ = json.Unmarshal(f.Msg, &ir)
					initHooks = ir.Request.Hooks
				}
			}
		}
	}
	for _, a := range argvAny {
		res.argv = append(res.argv, a.(string))
	}
	opts := optionsFromArgv(res.argv)

	pendingAnswer := func(ctx context.Context, id string) (json.RawMessage, bool) {
		for {
			if a, ok := answers[id]; ok {
				return a, true
			}
			select {
			case <-ctx.Done():
				return nil, false
			case <-time.After(5 * time.Millisecond):
			}
		}
	}
	// A request with no recorded answer was left open by the recording driver.
	hasAnswer := func(id string) bool { _, ok := answers[id]; return ok }

	opts.CanUseTool = func(ctx context.Context, req ToolPermissionRequest) (PermissionDecision, error) {
		id := idOf(req.Raw, frames)
		if !hasAnswer(id) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		a, _ := pendingAnswer(ctx, id)
		var r struct {
			Behavior     string          `json:"behavior"`
			UpdatedInput json.RawMessage `json:"updatedInput"`
			Message      string          `json:"message"`
			Interrupt    bool            `json:"interrupt"`
		}
		_ = json.Unmarshal(a, &r)
		if r.Behavior == "deny" {
			return Deny{Message: r.Message, Interrupt: r.Interrupt}, nil
		}
		return Allow{UpdatedInput: r.UpdatedInput}, nil
	}
	if len(initHooks) > 0 {
		opts.Hooks = map[HookEvent][]HookMatcher{}
		for ev, ms := range initHooks {
			for _, m := range ms {
				hm := HookMatcher{Matcher: m.Matcher}
				for range m.IDs {
					hm.Callbacks = append(hm.Callbacks, func(ctx context.Context, in HookInput) (HookOutput, error) {
						a := hookAnswer(frames, in)
						var out HookOutput
						_ = json.Unmarshal(a, &out)
						return out, nil
					})
				}
				opts.Hooks[HookEvent(ev)] = append(opts.Hooks[HookEvent(ev)], hm)
			}
		}
	}
	// the recorded argv, rendered by buildArgs, must agree on every flag the options model
	if _, hasSources := flagMap(res.argv)["--setting-sources"]; hasSources {
		got, err := buildArgs(&opts, "")
		if err != nil {
			t.Fatalf("buildArgs: %v", err)
		}
		want, have := flagMap(res.argv), flagMap(got)
		delete(have, "--permission-prompt-tool") // the recordings pass it; asserted below
		for k, v := range want {
			if k == "--permission-prompt-tool" {
				continue
			}
			if hv, ok := have[k]; !ok || hv != v {
				t.Errorf("argv drift on %s: recorded %q, buildArgs %q", k, v, hv)
			}
		}
		for k := range have {
			if _, ok := want[k]; !ok && k != "--permission-prompt-tool" {
				t.Errorf("buildArgs emits %s which the recording does not have", k)
			}
		}
		if want["--permission-prompt-tool"] == "stdio" && flagMap(got)["--permission-prompt-tool"] != "stdio" {
			t.Errorf("recorded run used --permission-prompt-tool stdio; buildArgs did not emit it")
		}
	}

	f := transporttest.New()
	res.fake = f
	opts.Transport = f

	// Sequencer. Responses to the SDK's own requests carry the SDK's ids, so
	// recorded ids are mapped to the ones actually written.
	idMap := map[string]string{}
	var idMu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// NewClient blocks on initialize, which needs the first out frames: run
	// the sequencer concurrently.
	type clientResult struct {
		c   *Client
		err error
	}
	cch := make(chan clientResult, 1)
	go func() {
		c, err := NewClient(ctx, opts)
		cch <- clientResult{c, err}
	}()

	var cl *Client
	var drain sync.WaitGroup
	var actWG sync.WaitGroup
	var mu sync.Mutex
	startDrain := func() {
		drain.Add(1)
		go func() {
			defer drain.Done()
			for m, err := range cl.Messages(ctx) {
				mu.Lock()
				if err != nil {
					var se *StreamError
					if errors.As(err, &se) {
						res.streamErr = append(res.streamErr, err)
					} else {
						res.endErr = err
					}
				} else {
					res.messages = append(res.messages, m)
				}
				mu.Unlock()
			}
		}()
	}
	feedOut := func(fr frame) {
		if fr.Dir == "out-raw" {
			var s string
			_ = json.Unmarshal(fr.Msg, &s)
			f.Feed(s)
			return
		}
		line := string(fr.Msg)
		m := parse(fr.Msg)
		if m["type"] == "control_response" {
			if id, _ := sub(m, "response", "request_id").(string); id != "" {
				idMu.Lock()
				actual, ok := idMap[id]
				idMu.Unlock()
				if ok {
					r := m["response"].(map[string]any)
					r["request_id"] = actual
					b, _ := json.Marshal(m)
					line = string(b)
				}
			}
		}
		f.Feed(line)
	}
	awaitWrite := func(expect frame) {
		w, err := f.NextWrite(ctx)
		if err != nil {
			t.Fatalf("expected an SDK write %s: %v", expect.Msg, err)
		}
		if got, want := normalize(w), normalize(expect.Msg); got != want {
			t.Fatalf("SDK wrote a different frame\n got  %s\n want %s", got, want)
		}
		em := parse(expect.Msg)
		if em["type"] == "control_request" {
			wm := parse(w)
			idMu.Lock()
			idMap[em["request_id"].(string)] = wm["request_id"].(string)
			idMu.Unlock()
		}
	}

	started := false
	seenUser := false
	_, resumed := flagMap(res.argv)["--resume"]
	ensureClient := func() {
		if cl != nil {
			return
		}
		select {
		case r := <-cch:
			if r.err != nil {
				t.Fatalf("NewClient: %v", r.err)
			}
			cl = r.c
			res.client = cl
			startDrain()
		case <-ctx.Done():
			t.Fatal("NewClient did not complete")
		}
	}
	act := func(fn func()) {
		actWG.Add(1)
		go func() {
			defer actWG.Done()
			fn()
		}()
	}
	for _, fr := range frames {
		switch fr.Dir {
		case "out", "out-raw":
			feedOut(fr)
		case "in":
			m := parse(fr.Msg)
			if !started {
				// the first in-frame is initialize, written by NewClient itself
				awaitWrite(fr)
				started = true
				continue
			}
			switch m["type"] {
			case "control_request":
				if st, _ := sub(m, "request", "subtype").(string); resumed && !seenUser && (st == "set_model" || st == "set_permission_mode") {
					break // R8: NewClient re-applies model and mode itself after a resume
				}
				ensureClient()
				st, _ := sub(m, "request", "subtype").(string)
				act(func() { res.addActionErr(&mu, doRequest(ctx, cl, st, m)) })
			case "user":
				seenUser = true
				ensureClient()
				text, _ := sub(m, "message", "content").(string)
				act(func() { res.addActionErr(&mu, cl.Send(ctx, UserText(text))) })
			case "control_response":
				// produced by a callback
			}
			awaitWrite(fr)
		case "meta":
			if parse(fr.Msg)["action"] != nil {
				ensureClient()
			}
			if sub(parse(fr.Msg), "action") == "close_stdin" {
				if err := cl.CloseInput(); err != nil {
					t.Errorf("CloseInput: %v", err)
				}
			}
			if sub(parse(fr.Msg), "action") == "sigkill_parent" {
				// the controller dies: nothing is answered, nothing is written
				before := len(f.Writes())
				if err := cl.Detach(); err != nil {
					t.Errorf("Detach: %v", err)
				}
				time.Sleep(20 * time.Millisecond)
				if len(f.Writes()) != before {
					t.Errorf("Detach wrote frames")
				}
			}
		}
	}
	ensureClient()
	if cl.writableErr() == nil { // mirror the driver's final stdin close
		_ = cl.CloseInput()
	}
	if haveExit {
		f.EOF(transport.ExitStatus{ExitCode: exitCode})
	} else {
		f.EOF(transport.ExitStatus{})
	}
	actWG.Wait()
	drain.Wait()
	res.events = f.Events()
	if rc.check != nil {
		rc.check(t, res)
	}
	_ = cl.Close()
	return res
}

func (r *replayResult) addActionErr(mu *sync.Mutex, err error) {
	mu.Lock()
	r.actionErr = append(r.actionErr, err)
	mu.Unlock()
}

// doRequest performs the SDK call that corresponds to a recorded request.
func doRequest(ctx context.Context, c *Client, subtype string, m map[string]any) error {
	req, _ := m["request"].(map[string]any)
	switch subtype {
	case "interrupt":
		_, err := c.Interrupt(ctx)
		return err
	case "mcp_status":
		_, err := c.MCPStatus(ctx)
		return err
	case "get_context_usage":
		_, err := c.ContextUsage(ctx)
		return err
	case "set_permission_mode":
		return c.SetPermissionMode(ctx, PermissionMode(req["mode"].(string)))
	case "set_model":
		return c.SetModel(ctx, req["model"].(string))
	case "rewind_files":
		_, err := c.RewindFiles(ctx, req["user_message_id"].(string), false)
		return err
	default:
		payload := map[string]any{}
		for k, v := range req {
			if k != "subtype" {
				payload[k] = v
			}
		}
		_, err := c.Request(ctx, subtype, payload)
		return err
	}
}

// idOf finds the request id of a can_use_tool request by matching the raw
// request object against the recorded out frames.
func idOf(raw json.RawMessage, frames []frame) string {
	for _, f := range frames {
		if f.Dir != "out" {
			continue
		}
		m := parse(f.Msg)
		if m["type"] != "control_request" {
			continue
		}
		rb, _ := json.Marshal(m["request"])
		if jsonEqual(rb, raw) {
			return m["request_id"].(string)
		}
	}
	return ""
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

// hookAnswer finds the recorded answer to the hook_callback carrying in.
func hookAnswer(frames []frame, in HookInput) json.RawMessage {
	for _, f := range frames {
		if f.Dir != "out" {
			continue
		}
		m := parse(f.Msg)
		if m["type"] != "control_request" || sub(m, "request", "subtype") != "hook_callback" {
			continue
		}
		inp, _ := json.Marshal(sub(m, "request", "input"))
		if jsonEqual(inp, in.Raw) {
			id := m["request_id"].(string)
			for _, g := range frames {
				gm := parse(g.Msg)
				if g.Dir == "in" && gm["type"] == "control_response" && sub(gm, "response", "request_id") == id {
					b, _ := json.Marshal(sub(gm, "response", "response"))
					return b
				}
			}
		}
	}
	return nil
}

// ---- cases ---------------------------------------------------------------

func countMessages(frames []frame) int {
	n := 0
	for _, f := range frames {
		if f.Dir != "out" {
			continue
		}
		switch parse(f.Msg)["type"] {
		case "control_request", "control_response", "control_cancel_request", "keep_alive":
		default:
			n++
		}
	}
	return n
}

func resultsOf(r *replayResult) (out []*protocol.Result) {
	for _, m := range r.messages {
		if v, ok := m.(*protocol.Result); ok {
			out = append(out, v)
		}
	}
	return
}

func replayCommon(t *testing.T, r *replayResult) {
	t.Helper()
	if len(r.streamErr) != 0 {
		t.Errorf("stream errors: %v", r.streamErr)
	}
	if want := countMessages(r.frames); len(r.messages) != want {
		t.Errorf("delivered %d messages, recording has %d", len(r.messages), want)
	}
	for _, m := range r.messages {
		if u, ok := m.(*protocol.Unknown); ok && u.Err != nil {
			t.Errorf("message of type %q failed typed decoding: %v", u.Type, u.Err)
		}
	}
}

func TestGoldenReplay(t *testing.T) {
	withCommon := func(f func(t *testing.T, r *replayResult)) func(*testing.T, *replayResult) {
		return func(t *testing.T, r *replayResult) {
			replayCommon(t, r)
			if f != nil {
				f(t, r)
			}
		}
	}
	lastInitSession := func(r *replayResult) string {
		s := ""
		for _, m := range r.messages {
			if i, ok := m.(*protocol.SystemInit); ok {
				s = i.SessionID
			}
		}
		return s
	}
	recordedResume := func(r *replayResult) string {
		return flagMap(r.argv)["--resume"]
	}
	cases := map[string]replayCase{
		"basic": {file: "core/basic.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			if rs := resultsOf(r); len(rs) != 1 || rs[0].Subtype != "success" || r.endErr != nil {
				t.Errorf("results=%v endErr=%v", rs, r.endErr)
			}
			if r.client.SessionID() == "" || r.client.SessionID() != lastInitSession(r) {
				t.Errorf("SessionID = %q", r.client.SessionID())
			}
			sawDelta := false
			for _, m := range r.messages {
				if se, ok := m.(*protocol.StreamEvent); ok {
					if _, ok := se.TextDelta(); ok {
						sawDelta = true
					}
				}
			}
			if !sawDelta {
				t.Error("no text_delta stream event decoded")
			}
		})},
		"multiturn": {file: "core/multiturn.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			rs := resultsOf(r)
			if len(rs) != 2 {
				t.Fatalf("results = %d", len(rs))
			}
			if rs[1].TotalCostUSD <= rs[0].TotalCostUSD {
				t.Errorf("total_cost_usd is cumulative: %v then %v", rs[0].TotalCostUSD, rs[1].TotalCostUSD)
			}
			inits := 0
			replays := 0
			for _, m := range r.messages {
				switch v := m.(type) {
				case *protocol.SystemInit:
					inits++
				case *protocol.User:
					if v.IsReplay {
						replays++
					}
				}
			}
			if inits != 2 || replays != 2 {
				t.Errorf("system/init per turn = %d, replay echoes = %d", inits, replays)
			}
		})},
		"perm allow with hook": {file: "core/perm.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			if len(resultsOf(r)) != 1 {
				t.Error("no result")
			}
			if !strings.Contains(strings.Join(r.events, "\n"), `"updatedInput":{"command":"echo rewritten-by-sdk"`) {
				t.Errorf("rewritten input not sent: %v", r.events)
			}
		})},
		"perm deny": {file: "core/perm.transcript.jsonl", mutate: func(fs []frame) []frame {
			out := make([]frame, len(fs))
			copy(out, fs)
			for i, f := range out {
				m := parse(f.Msg)
				if f.Dir == "in" && m["type"] == "control_response" && sub(m, "response", "response", "behavior") == "allow" {
					sub(m, "response")
					m["response"].(map[string]any)["response"] = map[string]any{"behavior": "deny", "message": "denied by test", "interrupt": true}
					b, _ := json.Marshal(m)
					out[i].Msg = b
				}
			}
			return out
		}, check: withCommon(func(t *testing.T, r *replayResult) {
			if !strings.Contains(strings.Join(r.events, "\n"), `"behavior":"deny","message":"denied by test","interrupt":true`) {
				t.Errorf("deny not written: %v", r.events)
			}
		})},
		"interrupt": {file: "core/interrupt.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			rs := resultsOf(r)
			if len(rs) != 1 || rs[0].Subtype != "error_during_execution" || rs[0].TerminalReason != "aborted_streaming" || !rs[0].IsError {
				t.Fatalf("results = %+v", rs)
			}
			var re *ResultError
			if !errors.As(r.endErr, &re) || re.ExitCode != 1 {
				t.Errorf("exit 1 after an error result must be a *ResultError, got %v", r.endErr)
			}
			aborted := false
			for _, m := range r.messages {
				if a, ok := m.(*protocol.Assistant); ok && a.Aborted {
					aborted = true
				}
			}
			if !aborted {
				t.Error("no aborted assistant frame")
			}
		})},
		"interrupt m0": {file: "m0/interrupt.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			var re *ResultError
			if !errors.As(r.endErr, &re) {
				t.Errorf("endErr = %v", r.endErr)
			}
		})},
		"control": {file: "core/control.transcript.jsonl", mutate: func(fs []frame) []frame {
			// This SDK does not host in-process MCP servers: it answers the CLI's
			// mcp_message requests with an error response, not the recording
			// driver's JSON-RPC results.
			out := make([]frame, len(fs))
			copy(out, fs)
			mcpIDs := map[string]bool{}
			for _, f := range out {
				m := parse(f.Msg)
				if f.Dir == "out" && sub(m, "request", "subtype") == "mcp_message" {
					mcpIDs[m["request_id"].(string)] = true
				}
			}
			for i, f := range out {
				m := parse(f.Msg)
				if f.Dir == "in" && m["type"] == "control_response" && mcpIDs[sub(m, "response", "request_id").(string)] {
					m["response"] = map[string]any{"subtype": "error", "request_id": sub(m, "response", "request_id"),
						"error": "Unsupported control request subtype: mcp_message"}
					b, _ := json.Marshal(m)
					out[i].Msg = b
				}
			}
			return out
		}, check: withCommon(func(t *testing.T, r *replayResult) {
			var ce []*ControlError
			for _, e := range r.actionErr {
				var c *ControlError
				if errors.As(e, &c) {
					ce = append(ce, c)
				}
			}
			subs := []string{}
			for _, c := range ce {
				subs = append(subs, c.Subtype+": "+c.Message)
			}
			sort.Strings(subs)
			joined := strings.Join(subs, "|")
			if !strings.Contains(joined, "bogus_subtype: Unsupported control request subtype: bogus_subtype") ||
				!strings.Contains(joined, "rewind_files: File rewinding is not enabled.") {
				t.Errorf("control errors = %v", subs)
			}
		})},
		"resume": {file: "m0/resume.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			if got := r.client.SessionID(); got == "" || got != recordedResume(r) {
				t.Errorf("resume must keep the session id: got %q, resumed %q", got, recordedResume(r))
			}
		})},
		"fork": {file: "m0/fork.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			if got := r.client.SessionID(); got == "" || got == recordedResume(r) {
				t.Errorf("fork must get a new session id: got %q, forked from %q", got, recordedResume(r))
			}
		})},
		"compact": {file: "m0/compact.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			var cb *protocol.CompactBoundary
			sawCompacting := false
			for _, m := range r.messages {
				switch v := m.(type) {
				case *protocol.CompactBoundary:
					cb = v
				case *protocol.SystemStatus:
					sawCompacting = sawCompacting || v.Status == "compacting"
				}
			}
			if cb == nil || cb.Trigger != "manual" || cb.PreTokens == 0 || cb.PostTokens == 0 || !sawCompacting {
				t.Errorf("compact_boundary=%+v compacting=%v", cb, sawCompacting)
			}
			if rs := resultsOf(r); len(rs) != 1 || rs[0].NumTurns != 0 {
				t.Errorf("results = %+v", rs)
			}
		})},
		"subagent": {file: "m0/subagent.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			sub, main := 0, 0
			for _, m := range r.messages {
				switch a := m.(type) {
				case *protocol.Assistant:
					if a.ParentToolUseID != "" {
						sub++
					} else {
						main++
					}
				case *protocol.User:
					if a.ParentToolUseID != "" { // the sub-agent's text arrives as a user frame
						sub++
					}
				}
			}
			if sub == 0 || main == 0 {
				t.Errorf("sub-agent frames %d, main %d", sub, main)
			}
		})},
		"mcp stdio": {file: "m0/mcp-stdio.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			echoed := false
			for _, m := range r.messages {
				if u, ok := m.(*protocol.User); ok {
					for _, b := range u.Message.Content.Blocks {
						if tr, ok := b.(*protocol.ToolResultBlock); ok && strings.Contains(tr.Content.String(), "echo: hello") {
							echoed = true
						}
					}
				}
			}
			if !echoed {
				t.Error("the local MCP server's tool_result was not delivered")
			}
		})},
		"plain": {file: "m0/plain.transcript.jsonl", check: withCommon(nil)},
		"approval held": {file: "m0/approval-hold.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			if len(resultsOf(r)) != 1 {
				t.Error("no result after the late answer")
			}
		})},
		"approval stdin close": {file: "m0/approval-stdin-close.transcript.jsonl", check: withCommon(func(t *testing.T, r *replayResult) {
			found := false
			for _, m := range r.messages {
				if u, ok := m.(*protocol.User); ok {
					for _, b := range u.Message.Content.Blocks {
						if tr, ok := b.(*protocol.ToolResultBlock); ok && tr.IsError &&
							strings.Contains(tr.Content.String(), "permission stream closed") {
							found = true
						}
					}
				}
			}
			if !found {
				t.Error("synthetic error tool_result not surfaced")
			}
			if !contains2(r.events, "close_input") {
				t.Errorf("events = %v", r.events)
			}
			if len(resultsOf(r)) != 1 || r.endErr != nil {
				t.Errorf("the model carries on after the failed approval: results=%d err=%v", len(resultsOf(r)), r.endErr)
			}
		})},
		"approval parent kill": {file: "m0/approval-parent-kill.transcript.jsonl", check: func(t *testing.T, r *replayResult) {
			// the controller dies with the approval open: Detach leaves the CLI
			// alone and writes no deny
			if contains2(r.events, "close") {
				t.Errorf("process must be left running: %v", r.events)
			}
			for _, e := range r.events {
				if strings.Contains(e, `"behavior"`) {
					t.Errorf("an approval was answered: %s", e)
				}
			}
		}},
	}
	for _, f := range []string{"default_trusted", "default_untrusted", "user", "empty", "akh_default_untrusted", "baseurl_default_untrusted"} {
		cases["hostile "+f] = replayCase{file: "hostile-repo/" + f + ".transcript.jsonl", check: withCommon(nil)}
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rc := cases[n]
		t.Run(n, func(t *testing.T) { runReplay(t, rc) })
	}
}
