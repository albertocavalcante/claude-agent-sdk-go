# Changelog

All notable changes to this project will be documented in this file.

## [0.3.0] - unreleased

A breaking rewrite on the CLI's bidirectional stream-json protocol. v0.2.x consumers can stay on v0.2.x.

### Breaking changes

- The one-shot `-p` wrapper and the per-query respawn `Client` are gone. `claude.NewClient` starts one long-lived process; `Send`, `Messages` (an `iter.Seq2`), `Turn` and `Query` replace the channel API.
- The SDK-side watcher "hooks" (`PreToolUse` and friends polled from the message stream) are removed. Permission and hook callbacks are real now: `Options.CanUseTool` and `Options.Hooks` answer the CLI's `can_use_tool` and `hook_callback` requests.
- Messages are `protocol.Message` values (`*protocol.Assistant`, `*protocol.Result`, ...). Every message and content block keeps `Raw`; unknown types and fields are never errors. `UnknownMessage` and `UnknownBlock` become `protocol.Unknown` and `protocol.UnknownBlock`.
- `internal/transport` is replaced by the public `claude/transport` package (`Transport` interface, process-group `Subprocess`) and `claude/transport/transporttest`.
- Errors: `*ResultError`, `*ProcessError`, `*ControlError`, `ErrClosed`, `ErrDetached`, `ErrInitializeTimeout`. `CLIError`, `ProtocolError` and `HookError` are removed.
- `Options` is reshaped (`Cwd`, `SettingSources`, `Settings`, `MCPServers` as `config.MCPServer`, `Resume`, `Continue`, `ForkSession`, `Env` as a `KEY=VALUE` slice with `EnvUnset`, ...).
- `Options.ExtraArgs` renders an empty value as an empty argument; use `ExtraFlags` for bare flags. New `Options.Tools` (`--tools`; empty non-nil disables all built-in tools).
- Go 1.27 is required.

### Security

- `SettingSources` defaults to the `user` source only. The CLI's default sources run a repository's hooks and `.mcp.json` servers and load its `env` and `apiKeyHelper` even in an untrusted workspace; a repo `env.ANTHROPIC_BASE_URL` leaked the OAuth bearer token to a local listener in testing.
- New `claude/config`: `FilteredProjectSettings` opts a project in without `env`, `*_BASE_URL`, `apiKeyHelper`, command helpers, repo-granted allow rules or unallowlisted hooks (allowlist by sha256 of canonical JSON), and a per-client 0600 MCP config writer.
- A live suite (`just live`) builds a hostile repository with canaries and a local listener and asserts that default `Options` trigger none of them.

### Features

- Multi-turn sessions in one process; `Interrupt` (the client stays usable), `SetPermissionMode`, `SetModel`, `MCPStatus`, `ContextUsage`, `RewindFiles`, `StopTask`, and `Request` for any other control subtype.
- `Close` denies open approvals with `interrupt: true`, then stops the whole process group (EOF, grace, SIGTERM, SIGKILL). `Detach` and `Attach` hand a running CLI to a new controller and recover its open approvals from the re-initialize reply.
- Resume, continue and fork (`--resume=`, `--continue`, `--fork-session`); the model and permission mode are re-applied after a resume.
- Unknown inbound control requests get an error response, so the CLI never blocks on them. The reader never waits for callbacks.
- Decoder caps of 16 MiB per line and 256 levels of JSON nesting (configurable), enforced before parsing and fuzzed.
- Golden replay of recorded CLI 2.1.316 sessions through a fake transport; the transcripts are scrubbed and guarded by a privacy test.
- `just ci`: gofmt, vet (also with `-tags claudecli`), golangci-lint v2 from a tool module, race tests, fuzz smoke.

### Review fixes

- Stdout is read to EOF: a slow consumer no longer loses the tail of a run (a force-close after the process exited now surfaces as `transport.ErrOutputTruncated`, never as EOF), and shutdown keeps draining stdout so a busy CLI can exit on EOF.
- `config.FilterProjectSettings` is an allowlist (only `permissions.deny`/`ask`, `includeCoAuthoredBy` and hash-allowlisted hooks pass; everything else is dropped and reported); symlinked or oversized settings files are refused.
- Field type drift in a frame (`error_code` as a number, `errors` as objects, ...) no longer downgrades a result, init or request or leaves a response unrouted.
- `claude.LaunchSpec` for runner-backed transports; value flags use the `--flag=value` form; tool entries containing commas are rejected.
- `Detach` suppresses every reply before touching the transport; `Close` bounds its deny writes; replies are cached and resent for requests the CLI still lists as pending; `Turn` skips results owed to an abandoned turn; callback error text is generic unless a `*UserError`.
- Temp files live in a private per-process directory with a stale sweeper.

### Documentation

- README rewritten. Fixed the TypeScript SDK link and the claim that unknown content blocks are silently skipped (they are preserved as `UnknownBlock`).

## [0.1.0] - 2026-03-26

### Bug Fixes

- Use go-version-file in CI instead of hardcoded matrix([e598fee](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/e598feee9d58efcdb745a1ac167484ae505a40f5))
- Use go 1.26.0 + toolchain directive, CI matrix 1.25/1.26, MCP cleanup([1240c8b](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/1240c8bd1938fff13f1a1e78b29bf5f8148e1cf1))

### Documentation

- Add Anthropic trademark acknowledgement to README([051f599](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/051f599b012303dd43a6f2c28287700e43a5212f))

### Features

- Initial Claude Agent SDK for Go (Phase 1)([454474a](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/454474a1319799ae238646209f1e9ededd4cb416))
- Add Phase 2 — ClaudeClient with multi-turn sessions and hooks([c300337](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/c3003373a87be74c1ee9562fef0547ea142ef43c))
- Phase 3 MCP tool registration + CI workflow([eede4ae](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/eede4ae458d82c99d871b458c33ce5dda708abec))
- Phase 4 — auto-cleanup MCP temp files, MCPConfigPath option([ba4b6ed](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/ba4b6ed6ff280126882868113d357c0f75c01623))
- Make SDK production-ready with fixes, constants, and tooling([ee0c44a](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/ee0c44a93c4d909b58a6c3c232f0296635e66620))
- Add CLIPrefixArgs and graceful SIGTERM shutdown([d90c750](https://github.com/albertocavalcante/claude-agent-sdk-go/commit/d90c750687075ae5d5bc07c84c855f9a6c39710f))

