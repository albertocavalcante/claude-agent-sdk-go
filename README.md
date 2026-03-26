# Claude Agent SDK for Go

[![CI](https://github.com/albertocavalcante/claude-agent-sdk-go/actions/workflows/ci.yml/badge.svg)](https://github.com/albertocavalcante/claude-agent-sdk-go/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/albertocavalcante/claude-agent-sdk-go.svg)](https://pkg.go.dev/github.com/albertocavalcante/claude-agent-sdk-go)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

A Go SDK for building agents powered by [Claude Code](https://docs.anthropic.com/en/docs/claude-code). Spawns the `claude` CLI as a subprocess and streams structured messages over Go channels.

> **Note:** This is a **community project** and is not officially maintained by Anthropic. For official SDKs, see the [TypeScript](https://github.com/anthropics/claude-code-sdk-python) and [Python](https://github.com/anthropics/claude-code-sdk-python) Agent SDKs.

## Prerequisites

- **Claude CLI** >= 2.0.0 (`npm install -g @anthropic-ai/claude-code`)
- **Go** >= 1.25

## Installation

```bash
go get github.com/albertocavalcante/claude-agent-sdk-go
```

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "os"

    "github.com/albertocavalcante/claude-agent-sdk-go/claude"
)

func main() {
    ctx := context.Background()

    for msg := range claude.Query(ctx, "What is 2+2? Reply in one word.", claude.Options{
        Model: claude.ModelHaiku,
    }) {
        if msg.Err != nil {
            fmt.Fprintf(os.Stderr, "Error: %v\n", msg.Err)
            os.Exit(1)
        }

        switch m := msg.Message.(type) {
        case *claude.AssistantMessage:
            for _, block := range m.Content {
                if tb, ok := block.(*claude.TextBlock); ok {
                    fmt.Print(tb.Text)
                }
            }
        case *claude.ResultMessage:
            fmt.Printf("\n---\nTokens: %d in, %d out | Cost: $%.4f\n",
                m.InputTokens, m.OutputTokens, m.Cost)
        }
    }
}
```

## Persistent Sessions

```go
client := claude.NewClient(claude.Options{
    Model: claude.ModelSonnet,
})
defer client.Close()

// First turn
for msg := range client.Query(ctx, "Remember: my name is Alice") {
    // handle messages...
}

// Second turn resumes the same session
for msg := range client.Query(ctx, "What is my name?") {
    // handle messages...
}

fmt.Println("Session:", client.SessionID())
```

## Hooks

Register callbacks for lifecycle events:

```go
client := claude.NewClient(claude.Options{
    Hooks: []claude.HookRegistration{
        {
            Event: claude.HookPreToolUse,
            Callback: func(ctx context.Context, e claude.HookInput) (claude.HookOutput, error) {
                fmt.Printf("Tool: %s\n", e.ToolName)
                return claude.HookOutput{}, nil
            },
        },
        {
            Event:       claude.HookPostToolUse,
            ToolPattern: "^Bash$", // only match Bash tool
            Callback: func(ctx context.Context, e claude.HookInput) (claude.HookOutput, error) {
                fmt.Printf("Bash output: %s\n", e.ToolOutput)
                return claude.HookOutput{}, nil
            },
        },
    },
})
```

### Hook Events

| Event           | When                         | HookInput fields set                    |
|-----------------|------------------------------|-----------------------------------------|
| `HookMessage`   | Every message                | Event, SessionID, Message               |
| `HookPreToolUse`| Before tool execution        | + ToolName, ToolInput                   |
| `HookPostToolUse`| After tool execution        | + ToolName, ToolOutput                  |
| `HookResult`    | Final result received        | Event, SessionID, Message               |

## MCP Server Integration

Connect external MCP tool servers to give Claude access to custom tools:

```go
client := claude.NewClient(claude.Options{
    MCPServers: []claude.MCPServerConfig{
        {
            Name:    "filesystem",
            Command: "npx",
            Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
        },
    },
})
```

The SDK writes a temporary MCP config file and passes it to the CLI via `--mcp-config`. Cleanup is automatic.

## API Reference

### Options

| Field                | Type                | Description                                      |
|----------------------|---------------------|--------------------------------------------------|
| `Model`              | `string`            | Model name or constant (`ModelOpus`, `ModelSonnet`, `ModelHaiku`) |
| `SystemPrompt`       | `string`            | Override the default system prompt                |
| `AppendSystemPrompt` | `string`            | Append to the default system prompt               |
| `AllowedTools`       | `[]string`          | Tool allowlist                                    |
| `DisallowedTools`    | `[]string`          | Tool denylist                                     |
| `MaxThinkingTokens`  | `int`               | Extended thinking token budget                    |
| `MaxTurns`           | `int`               | Max agent loop turns                              |
| `WorkingDirectory`   | `string`            | Working directory for the CLI subprocess          |
| `PermissionMode`     | `string`            | `PermissionDefault`, `PermissionAcceptEdits`, `PermissionBypassPermissions` |
| `CLIPath`            | `string`            | Path to the `claude` binary (default: from PATH)  |
| `Env`                | `map[string]string` | Extra environment variables for the CLI process   |
| `MCPServers`         | `[]MCPServerConfig` | External MCP servers to connect to                |
| `MCPConfigPath`      | `string`            | Path to pre-existing MCP config (takes precedence over MCPServers) |

### Message Types

| Type               | Description                                  |
|--------------------|----------------------------------------------|
| `AssistantMessage` | Response from Claude with content blocks     |
| `UserMessage`      | User turn (tool results, etc.)               |
| `ResultMessage`    | Final result with usage stats and session ID |
| `SystemMessage`    | System events (init, compaction, etc.)        |
| `UnknownMessage`   | Forward-compatible wrapper for new types     |

### Content Block Types

| Type              | Description               |
|-------------------|---------------------------|
| `TextBlock`       | Text content              |
| `ToolUseBlock`    | Tool invocation           |
| `ToolResultBlock` | Tool execution result     |
| `ThinkingBlock`   | Extended thinking content |

### Error Types

| Type            | Description                  | Helper          |
|-----------------|------------------------------|-----------------|
| `CLIError`      | CLI returned an error        | `IsCLIError()`  |
| `ProtocolError` | JSON parsing failure         | `IsProtocolError()` |
| `ProcessError`  | Subprocess exited abnormally | `IsProcessError()`, `ExitCode()` |

## Architecture

```
Your Go app  -->  claude-agent-sdk-go  -->  claude CLI  -->  Anthropic API
                  (this library)            (subprocess)
```

1. Spawns the `claude` CLI binary as a subprocess
2. Uses `--output-format stream-json` for structured streaming output
3. Parses JSON lines from stdout into typed Go structs
4. Delivers messages over a Go channel for idiomatic consumption

## Forward Compatibility

- **Unknown message types** are returned as `*UnknownMessage` with the raw JSON preserved
- **Unknown content block types** within messages are silently skipped

This ensures your application won't break when the CLI adds new message or content types.

## Development

```bash
# Run all checks
just check

# Individual commands
just fmt        # format code
just lint       # run go vet
just test       # run tests with race detector
just build      # build all packages
```

## Zero Dependencies

This SDK has no external dependencies -- only the Go standard library.

## License

[MIT](LICENSE)

## Trademarks

Claude and Claude Code are trademarks of [Anthropic, PBC](https://www.anthropic.com). This project is not affiliated with or endorsed by Anthropic.
