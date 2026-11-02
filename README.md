# Claude Agent SDK for Go

[![CI](https://github.com/albertocavalcante/claude-agent-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/claude-agent-sdk-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/albertocavalcante/claude-agent-sdk-go.svg)](https://pkg.go.dev/github.com/albertocavalcante/claude-agent-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Go SDK for building agents on [Claude Code](https://docs.anthropic.com/en/docs/claude-code). It runs the `claude` CLI as a long-lived subprocess and talks to it over the CLI's bidirectional `stream-json` protocol: multi-turn sessions in one process, tool-permission and hook callbacks, interrupts, and typed messages.

> **Note:** This is a **community project** and is not officially maintained by Anthropic. For the official SDKs, see [TypeScript](https://github.com/anthropics/claude-agent-sdk-typescript) and [Python](https://github.com/anthropics/claude-agent-sdk-python).

v0.3 is a breaking rewrite of v0.2. See the [changelog](CHANGELOG.md).

## Prerequisites

- **Claude Code CLI**, tested with Claude Code 2.1.316 (`npm install -g @anthropic-ai/claude-code`). Other versions are untested; unknown fields and message types are preserved rather than rejected.
- **Go** >= 1.27

## Installation

```bash
go get github.com/albertocavalcante/claude-agent-sdk-go
```

## Quick start

One-shot:

```go
for msg, err := range claude.Query(ctx, "What is 2+2? Reply in one word.", claude.Options{Model: "haiku"}) {
    if err != nil {
        log.Fatal(err)
    }
    if a, ok := msg.(*protocol.Assistant); ok {
        fmt.Print(a.Text())
    }
}
```

A session that lives across turns:

```go
c, err := claude.NewClient(ctx, claude.Options{Model: "haiku"}) // spawn + initialize
if err != nil {
    log.Fatal(err)
}
defer c.Close()

res, err := c.Turn(ctx, claude.UserText("Remember: the word is banana.")) // Send + drain to Result
res, err = c.Turn(ctx, claude.UserText("What was the word?"))            // same process, same session
fmt.Println(res.Result, c.SessionID())
```

Streaming with full control:

```go
c.Send(ctx, claude.UserText("Count to 300."))
for msg, err := range c.Messages(ctx) { // iter.Seq2[protocol.Message, error]
    if err != nil { /* *StreamError is non-fatal; anything else ends the stream */ }
    switch m := msg.(type) {
    case *protocol.StreamEvent: // needs Options.IncludePartialMessages
    case *protocol.Result:      // end of the turn
    }
}
c.Interrupt(ctx) // acks first, then result{error_during_execution, aborted_streaming}; the client stays usable
```

See [`examples/`](examples) for permission and hook callbacks (`examples/hooks`) and MCP servers (`examples/mcp`).

## Packages

| Package | What it is |
|---|---|
| `claude` | `Client`, `Query`, `Attach`, `Options` |
| `claude/protocol` | Wire types only, no I/O: messages, content blocks, control envelopes, a decoder with size and depth caps |
| `claude/transport` | The `Transport` interface and the process-group `Subprocess` implementation |
| `claude/transport/transporttest` | Scripted fake transport for tests |
| `claude/config` | Safe launch policy: setting sources, `FilteredProjectSettings`, MCP config writer |

## Security defaults

**By default the CLI loads a repository's `.claude/settings.json`, `.mcp.json`, hooks, `env` and `apiKeyHelper`, even in an untrusted workspace.** Workspace trust only gates permission allow rules. In testing, a repo's `env.ANTHROPIC_BASE_URL` made the CLI send the user's OAuth bearer token to a host the repo chose, repo hooks ran commands at session start, and a repo `apiKeyHelper` replaced the login.

This SDK therefore differs from the CLI's defaults:

- **`Options.SettingSources` defaults to the `user` source only** (`--setting-sources user`). Repo hooks, MCP servers, env, `apiKeyHelper`, `CLAUDE.md`, skills and agents are not loaded. Set it explicitly (`[]config.SettingSource{...}`) to change that; a non-nil empty slice loads nothing.
- **To opt a project in, use `config.FilteredProjectSettings`** instead of adding the `project` source:

  ```go
  path, cleanup, err := config.FilteredProjectSettings(repoDir, nil) // nil: allow no hooks
  defer cleanup()
  c, err := claude.NewClient(ctx, claude.Options{Cwd: repoDir, Settings: path})
  ```

  It reads the repo's `settings.json` and `settings.local.json` (symlinks and files over 1 MiB are refused) and writes a 0600 file for `--settings`. The filter is an **allowlist**: exactly these pass, and everything else is dropped and reported in `FilterResult.Dropped`:

  - `permissions.deny` and `permissions.ask` rules (they can only make the session stricter),
  - `includeCoAuthoredBy`,
  - hook groups whose sha256 over canonical JSON you allowlisted (`config.ListHooks` shows the hashes).

  So `env`, `*_BASE_URL`, `apiKeyHelper` and the other command helpers, `statusLine`, `fileSuggestion`, plugins and marketplaces, `sandbox`, MCP keys, `model`, `permissions.allow`/`defaultMode`/`additionalDirectories` and any key a future CLI adds do not pass. `cleanupPeriodDays` is dropped on purpose: it deletes transcripts globally, and `--settings` outranks the user's own settings. A hook allowlist entry hashes the hook's command string, not the script it runs; if the script lives in the repo, editing it changes behaviour without changing the hash. Allowlisting a hook gives that repo code execution; treat it that way in your UI.
- Read-only Bash commands and the Agent tool are auto-approved by the CLI and never reach `CanUseTool`; the user's own allow rules apply too. Do not tell users that every tool call is approved.
- The generated MCP config file (it can hold tokens) and the filtered settings are 0600, with unique names per client.
- Tool lists (`Tools`, `AllowedTools`, `DisallowedTools`) must not contain entries with commas, because the CLI splits on them; an empty non-nil `Tools` disables all built-in tools.
- `Env` and `EnvUnset` give full control of the child's environment, for example `EnvUnset: []string{"CLAUDE_CODE_OAUTH_TOKEN"}`. `CLAUDECODE` is always removed.

## Callbacks

`Options.CanUseTool` launches the CLI with `--permission-prompt-tool stdio`. Each request runs on its own goroutine with a context cancelled by the CLI's `control_cancel_request`, by `Close` and by `Detach`. The CLI waits for the answer without a timeout, so decide your own policy.

```go
CanUseTool: func(ctx context.Context, r claude.ToolPermissionRequest) (claude.PermissionDecision, error) {
    return claude.Allow{UpdatedInput: map[string]any{"command": "echo hi"}}, nil // nil UpdatedInput echoes the original input
    // or: claude.Deny{Message: "no", Interrupt: true}
}
```

`Options.Hooks` registers hook callbacks at initialize; the SDK assigns the `hook_N` ids. `Options.OnElicitation` answers MCP elicitations (default: decline). Any inbound control request the SDK does not know gets an error response, so the CLI never blocks on it. A callback that returns an error makes the SDK send a generic error text to the CLI (and so to the model); return `claude.NewUserError("...")` for a message that may be shown. A reply that cannot be written is reported to `Options.OnReplyError`, and a request the CLI still lists as pending after `Reinitialize` is answered again.

## Close vs Detach

- **`Close()`** ends the session. Every open `can_use_tool` request is first answered `deny` with `interrupt: true` (otherwise the CLI turns the closed stream into a failed tool call and the model may retry). Then stdin is closed, the process gets a grace period, then SIGTERM and finally SIGKILL for its **whole process group** (MCP servers and Bash children included).
- **`Detach()`** is for a controller that restarts while the CLI keeps running behind a transport that owns the pipes (a runner). It stops reading and writing, cancels callback contexts **without answering them**, and calls `Transport.Detach()`. The default `Subprocess` transport returns `transport.ErrDetachUnsupported` because it owns the process.
- **`claude.Attach(ctx, t, opts)`** is the other half. It re-sends `initialize` to the running CLI, reads the `pending_permission_requests` the CLI reports next to the reply, and runs each through `CanUseTool` once per request id. `Client.Pending()` returns what was found. A hook in flight at that moment is denied and retried once by the CLI.

If a controller dies without `Detach`, the CLI fails the open approval, may retry the tool, and exits within seconds of losing stdin.

## Runner-backed transports

A transport that starts the CLI somewhere else (a runner that owns the pipes) must launch from `claude.LaunchSpec(opts)`. It returns the executable, argv, complete environment and working directory with every safe default applied (user-only setting sources, `CLAUDECODE` and `EnvUnset` keys removed, entrypoint and session-state variables set, a per-client 0600 MCP config file) plus a `Cleanup`. `Subprocess` is launched from the same spec. Temporary files live in a private 0700 per-process directory (`config.RunDir`); call `config.SweepStale` at daemon start to remove the leftovers of crashed processes.

## Errors

- `*ResultError{Result, ExitCode}`: the CLI exited 1 after an error result (for instance after an interrupted turn). Not a transport failure.
- `*ProcessError{ExitCode, Signal, StderrTail}`: any other abnormal exit. The last 64 KiB of stderr are kept in the field; `Error()` prints only a short, secret-masked last line.
- `*ControlError{Subtype, Message}`: the CLI answered a control request with an error.
- `ErrClosed`, `ErrDetached`, `ErrInitializeTimeout`.
- `*StreamError`: one stdout line was malformed or over the size cap (default 16 MiB); the stream continues.

## Forward compatibility

Unknown message types decode to `*protocol.Unknown`, unknown system subtypes to `*protocol.SystemUnknown`, and unknown content blocks to `*protocol.UnknownBlock`. Every message and block keeps its original JSON in `Raw`, and unknown fields are never errors, so a newer CLI does not break an older SDK. Non-JSON lines on stdout are skipped. A line over the size cap or nested deeper than 256 levels is rejected before it is parsed.

## Backpressure

One goroutine reads stdout into a bounded queue (`Options.MessageBuffer`, default 256). It blocks only when that queue is full and nobody consumes `Messages`; it never waits for callbacks. While it is blocked, control responses are not processed either, so keep draining `Messages` (or use `Turn`) while a turn runs.

## Testing

`transporttest.Fake` replays recorded stdout lines and captures what the SDK writes. The golden tests replay real CLI 2.1.316 sessions (scrubbed of personal data, guarded by a privacy test) through it. A live suite against a real CLI sits behind a build tag:

```bash
CLAUDE_CODE_OAUTH_TOKEN=... just live   # a few cents of haiku usage
```

## Development

```bash
just ci     # fmt-check, vet, golangci-lint, race tests, fuzz smoke
just test   # tests
just live   # live suite (needs CLAUDE_CODE_OAUTH_TOKEN)
```

## Dependencies

None beyond the Go standard library.

## License

[MIT](LICENSE)

## Trademarks

Claude and Claude Code are trademarks of [Anthropic, PBC](https://www.anthropic.com). This project is not affiliated with or endorsed by Anthropic.
