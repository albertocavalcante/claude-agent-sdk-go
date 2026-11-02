// Command record-fixtures records the golden transcripts under
// claude/testdata/transcripts/m0 and core/multiturn from a real CLI, driven
// through this SDK. Everything runs in scratch directories with an isolated
// CLAUDE_CONFIG_DIR and HOME and with claude.ai connectors disabled, so the
// recordings carry nothing personal except the account block of the initialize
// reply, which the scrubber redacts.
//
// Usage (the token stays in the environment, never on disk):
//
//	export CLAUDE_CODE_OAUTH_TOKEN=...
//	go run ./scripts/record-fixtures <raw-out-dir>
//	SCRUB_MAP=... python3 -I scripts/scrub-fixtures/scrub.py <raw-out-dir>
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
)

// recorder wraps a transport and logs every line in the transcript format
// {"t": seconds, "dir": "in"|"out"|"out-raw"|"meta", "msg": ...}.
type recorder struct {
	transport.Transport
	mu  sync.Mutex
	w   *os.File
	t0  time.Time
	rep *strings.Replacer

	inputClosed atomic.Bool
}

func (r *recorder) log(dir string, msg any) {
	b, _ := json.Marshal(map[string]any{"t": float64(time.Since(r.t0).Milliseconds()) / 1000, "dir": dir, "msg": msg})
	r.mu.Lock()
	defer r.mu.Unlock()
	_, _ = r.w.WriteString(r.rep.Replace(string(b)) + "\n")
}

func (r *recorder) ReadLine(ctx context.Context) ([]byte, error) {
	line, err := r.Transport.ReadLine(ctx)
	if err == nil {
		if len(line) > 0 && line[0] == '{' && json.Valid(line) {
			r.log("out", json.RawMessage(line))
		} else {
			r.log("out-raw", string(line))
		}
	}
	return line, err
}

func (r *recorder) WriteLine(line []byte) error {
	if !r.inputClosed.Load() { // a late deny after EOF is not a frame the CLI saw
		r.log("in", json.RawMessage(line))
	}
	return r.Transport.WriteLine(line)
}

func (r *recorder) CloseInput() error {
	r.inputClosed.Store(true)
	return r.Transport.CloseInput()
}

// Detach pretends to hand the pipes over; the recorder keeps them.
func (r *recorder) Detach() error { return nil }

type env struct {
	home, cfg, cwd string
	mcpBin         string
	rep            *strings.Replacer
	base           []string
}

type scenario struct {
	name string
	file string // relative to the out dir
	opts func(e *env, s *state) claude.Options
	run  func(ctx context.Context, e *env, s *state, c *claude.Client, r *recorder, sp *transport.Subprocess) (finish bool, err error)
}

// state carries session ids between scenarios.
type state struct{ plain, fork string }

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: record-fixtures <raw-out-dir> [scenario...]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(out string, only []string) error {
	token := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")
	if token == "" {
		return errors.New("CLAUDE_CODE_OAUTH_TOKEN is not set")
	}
	tmp, err := os.MkdirTemp("", "record-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	resolve := func(p string) string {
		r, e := filepath.EvalSymlinks(p)
		if e != nil {
			return p
		}
		return r
	}
	e := &env{home: filepath.Join(tmp, "home"), cfg: filepath.Join(tmp, "home", ".claude"), cwd: filepath.Join(tmp, "work")}
	for _, d := range []string{e.home, e.cfg, e.cwd} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// the MCP test server
	e.mcpBin = filepath.Join(tmp, "mcpserver")
	if b, err := exec.CommandContext(context.Background(), "go", "build", "-o", e.mcpBin, "./scripts/record-fixtures/mcpserver").CombinedOutput(); err != nil {
		return fmt.Errorf("building the MCP server: %v: %s", err, b)
	}
	e.rep = strings.NewReplacer(
		resolve(e.cfg), "/home/user/.claude", e.cfg, "/home/user/.claude",
		resolve(e.home), "/home/user", e.home, "/home/user",
		resolve(e.cwd), "/work", e.cwd, "/work",
		resolve(tmp), "/work/tmp", tmp, "/work/tmp",
	)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "CLAUDE") && !strings.HasPrefix(kv, "ANTHROPIC") && !strings.HasPrefix(kv, "HOME=") && !strings.HasPrefix(kv, "ENABLE_CLAUDEAI") {
			e.base = append(e.base, kv)
		}
	}
	e.base = append(e.base, "CLAUDE_CONFIG_DIR="+e.cfg, "HOME="+e.home, "CLAUDE_CODE_OAUTH_TOKEN="+token, "ENABLE_CLAUDEAI_MCP_SERVERS=false")

	st := &state{}
	for _, sc := range scenarios() {
		if len(only) > 0 && !contains(only, sc.name) {
			continue
		}
		fmt.Println("recording", sc.name)
		if err := record(out, e, st, sc); err != nil {
			return fmt.Errorf("%s: %w", sc.name, err)
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func record(out string, e *env, st *state, sc scenario) error {
	path := filepath.Join(out, sc.file)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	opts := sc.opts(e, st)
	opts.Model = "haiku"
	opts.Env = e.base
	opts.Cwd = e.cwd
	spec, err := claude.LaunchSpec(opts)
	if err != nil {
		return err
	}
	defer spec.Cleanup()
	sp := &transport.Subprocess{Path: spec.Path, Args: spec.Args, Dir: spec.Dir, Env: spec.Env}
	rec := &recorder{Transport: sp, w: f, t0: time.Now(), rep: e.rep}
	rec.log("meta", map[string]any{"argv": append([]string{"claude"}, spec.Args...), "cwd": e.cwd})
	opts.Transport = rec

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	c, err := claude.NewClient(ctx, opts)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	finish, err := sc.run(ctx, e, st, c, rec, sp)
	if err != nil {
		return err
	}
	if !finish {
		return nil
	}
	_ = c.CloseInput()
	drain(ctx, c)
	wctx, wc := context.WithTimeout(ctx, 30*time.Second)
	defer wc()
	status, err := sp.Wait(wctx)
	if err != nil {
		return err
	}
	rec.log("meta", map[string]any{"exit_code": status.ExitCode})
	return nil
}

func drain(ctx context.Context, c *claude.Client) {
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for m, err := range c.Messages(dctx) {
		_, _ = m, err
	}
}

func allowAll(context.Context, claude.ToolPermissionRequest) (claude.PermissionDecision, error) {
	return claude.Allow{}, nil
}

func turn(ctx context.Context, c *claude.Client, prompt string) (*protocol.Result, error) {
	res, err := c.Turn(ctx, claude.UserText(prompt))
	if err != nil {
		return nil, err
	}
	if res.IsError && res.Subtype != "error_during_execution" {
		return nil, fmt.Errorf("result %s: %s", res.Subtype, res.Result)
	}
	return res, nil
}

const touchPrompt = "Use the Bash tool to run exactly: touch approval-marker.txt   -- then reply with the word done."

func scenarios() []scenario {
	std := func(max int) func(*env, *state) claude.Options {
		return func(*env, *state) claude.Options {
			return claude.Options{MaxTurns: max, CanUseTool: allowAll, IncludePartialMessages: false}
		}
	}
	return []scenario{
		{name: "multiturn", file: "core/multiturn.transcript.jsonl",
			opts: func(*env, *state) claude.Options {
				return claude.Options{MaxTurns: 1, Tools: []string{}, SystemPrompt: "Be terse.", SettingSources: []config.SettingSource{}}
			},
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				for _, p := range []string{"Say A", "Say B"} {
					if _, err := turn(ctx, c, p); err != nil {
						return false, err
					}
				}
				return true, nil
			}},
		{name: "plain", file: "m0/plain.transcript.jsonl",
			opts: func(*env, *state) claude.Options {
				return claude.Options{MaxTurns: 1, CanUseTool: allowAll, IncludePartialMessages: true}
			},
			run: func(ctx context.Context, _ *env, st *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				if _, err := turn(ctx, c, "Reply with the single word: pong"); err != nil {
					return false, err
				}
				st.plain = c.SessionID()
				return true, nil
			}},
		{name: "resume", file: "m0/resume.transcript.jsonl",
			opts: func(_ *env, st *state) claude.Options {
				return claude.Options{MaxTurns: 1, CanUseTool: allowAll, Resume: st.plain}
			},
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				_, err := turn(ctx, c, "What single word did you reply with in your previous answer? Reply with only that word.")
				return err == nil, err
			}},
		{name: "fork", file: "m0/fork.transcript.jsonl",
			opts: func(_ *env, st *state) claude.Options {
				return claude.Options{MaxTurns: 1, CanUseTool: allowAll, Resume: st.plain, ForkSession: true}
			},
			run: func(ctx context.Context, _ *env, st *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				if _, err := turn(ctx, c, "This is a fork. Reply with the single word: forked"); err != nil {
					return false, err
				}
				st.fork = c.SessionID()
				return true, nil
			}},
		{name: "compact", file: "m0/compact.transcript.jsonl",
			opts: func(_ *env, st *state) claude.Options {
				return claude.Options{MaxTurns: 1, CanUseTool: allowAll, Resume: st.fork}
			},
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				_, err := turn(ctx, c, "/compact")
				return err == nil, err
			}},
		{name: "subagent", file: "m0/subagent.transcript.jsonl", opts: std(3),
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				_, err := turn(ctx, c, "Use the Agent tool (subagent_type general-purpose) to compute 17*3. The subagent must not use any tools. Then reply with just the number.")
				return err == nil, err
			}},
		{name: "interrupt", file: "m0/interrupt.transcript.jsonl",
			opts: func(*env, *state) claude.Options {
				return claude.Options{MaxTurns: 1, CanUseTool: allowAll, IncludePartialMessages: true, Tools: []string{}}
			},
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				if err := c.Send(ctx, claude.UserText("Count from 1 to 300, one number per line, no other text.")); err != nil {
					return false, err
				}
				for m, err := range c.Messages(ctx) {
					if err != nil {
						return false, err
					}
					if se, ok := m.(*protocol.StreamEvent); ok {
						if _, ok := se.TextDelta(); ok {
							break
						}
					}
				}
				if _, err := c.Interrupt(ctx); err != nil {
					return false, err
				}
				for m, err := range c.Messages(ctx) {
					if err != nil {
						return false, err
					}
					if _, ok := m.(*protocol.Result); ok {
						break
					}
				}
				return true, nil
			}},
		{name: "mcp-stdio", file: "m0/mcp-stdio.transcript.jsonl",
			opts: func(e *env, _ *state) claude.Options {
				return claude.Options{MaxTurns: 4, CanUseTool: allowAll, StrictMCPConfig: true,
					MCPServers: map[string]config.MCPServer{"example": {Command: e.mcpBin}}}
			},
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				for range 2 { // poll until the server is connected, as a host would
					if _, err := c.MCPStatus(ctx); err != nil {
						return false, err
					}
					time.Sleep(time.Second)
				}
				_, err := turn(ctx, c, "Call the tool mcp__example__echo exactly once with text hello. Then reply with only the tool's output.")
				return err == nil, err
			}},
		{name: "approval-hold", file: "m0/approval-hold.transcript.jsonl",
			opts: func(*env, *state) claude.Options {
				return claude.Options{MaxTurns: 3, Tools: []string{"Bash"}, CanUseTool: func(ctx context.Context, _ claude.ToolPermissionRequest) (claude.PermissionDecision, error) {
					select { // held with no frames on the wire
					case <-time.After(20 * time.Second):
					case <-ctx.Done():
					}
					return claude.Allow{}, nil
				}}
			},
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, _ *recorder, _ *transport.Subprocess) (bool, error) {
				_, err := turn(ctx, c, touchPrompt)
				return err == nil, err
			}},
		{name: "approval-stdin-close", file: "m0/approval-stdin-close.transcript.jsonl", opts: blocking,
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, r *recorder, _ *transport.Subprocess) (bool, error) {
				if err := openApproval(ctx, c); err != nil {
					return false, err
				}
				r.log("meta", map[string]any{"action": "close_stdin"})
				return true, c.CloseInput()
			}},
		{name: "approval-parent-kill", file: "m0/approval-parent-kill.transcript.jsonl", opts: blocking,
			run: func(ctx context.Context, _ *env, _ *state, c *claude.Client, r *recorder, sp *transport.Subprocess) (bool, error) {
				if err := openApproval(ctx, c); err != nil {
					return false, err
				}
				r.log("meta", map[string]any{"action": "sigkill_parent"})
				if err := c.Detach(); err != nil {
					return false, err
				}
				time.Sleep(time.Second)
				return false, sp.Close() // the controller is gone; stop the CLI ourselves
			}},
	}
}

// blocking leaves every approval unanswered.
func blocking(*env, *state) claude.Options {
	return claude.Options{MaxTurns: 3, Tools: []string{"Bash"}, CanUseTool: func(ctx context.Context, _ claude.ToolPermissionRequest) (claude.PermissionDecision, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
}

// openApproval sends the prompt and waits until the CLI is asking.
func openApproval(ctx context.Context, c *claude.Client) error {
	if err := c.Send(ctx, claude.UserText(touchPrompt)); err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	for m, err := range c.Messages(dctx) {
		if err != nil {
			return err
		}
		if s, ok := m.(*protocol.SessionStateChanged); ok && s.State == "requires_action" {
			time.Sleep(2 * time.Second)
			return nil
		}
	}
	return errors.New("the CLI never asked for approval")
}
