# Claude Agent SDK for Go

A Go-idiomatic SDK for building agents powered by [Claude Code](https://docs.anthropic.com/en/docs/claude-code). This SDK spawns the `claude` CLI as a subprocess and streams structured messages back over Go channels.

> **Note:** This is a **community project** and is not officially maintained by Anthropic. For official SDKs, see the [TypeScript](https://github.com/anthropics/claude-code-sdk-python) and [Python](https://github.com/anthropics/claude-code-sdk-python) Agent SDKs.

## Prerequisites

- **Claude CLI** >= 2.0.0 (install via `npm install -g @anthropic-ai/claude-code`)
- **Go** >= 1.21

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
        Model: "haiku",
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

## API Reference

### `claude.Query`

```go
func Query(ctx context.Context, prompt string, opts Options) <-chan MessageOrError
```

Sends a one-shot prompt to the Claude CLI and returns a channel that streams `MessageOrError` values. The channel is closed when the conversation ends or the context is cancelled.

### `claude.Options`

| Field                | Type                | Description                                      |
|----------------------|---------------------|--------------------------------------------------|
| `Model`              | `string`            | Model name (e.g. `"opus-4-6"`, `"sonnet-4-6"`)  |
| `SystemPrompt`       | `string`            | Override the default system prompt                |
| `AppendSystemPrompt` | `string`            | Append to the default system prompt               |
| `AllowedTools`       | `[]string`          | Tool allowlist                                    |
| `DisallowedTools`    | `[]string`          | Tool denylist                                     |
| `MaxThinkingTokens`  | `int`               | Extended thinking token budget                    |
| `MaxTurns`           | `int`               | Max agent loop turns                              |
| `WorkingDirectory`   | `string`            | Working directory for the CLI subprocess          |
| `PermissionMode`     | `string`            | `"default"`, `"acceptEdits"`, `"bypassPermissions"` |
| `CLIPath`            | `string`            | Path to the `claude` binary (default: from PATH)  |
| `Env`                | `map[string]string` | Extra environment variables for the CLI process   |
| `MCPServers`         | `[]MCPServerConfig` | External MCP servers to connect to                |

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

### MCP Server Configuration

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

The SDK writes a temporary MCP config file and passes it to the CLI via `--mcp-config`.

| Field     | Type                | Description                              |
|-----------|---------------------|------------------------------------------|
| `Name`    | `string`            | Server identifier                        |
| `Command` | `string`            | Executable to run                        |
| `Args`    | `[]string`          | Command-line arguments                   |
| `Env`     | `map[string]string` | Environment variables for the server     |
| `CWD`     | `string`            | Working directory for the server process |

## Architecture

This SDK follows the same architecture as Anthropic's official TypeScript and Python Agent SDKs:

1. Spawns the `claude` CLI binary as a subprocess
2. Uses `--output-format stream-json` for structured streaming output
3. Parses JSON lines from stdout into typed Go structs
4. Delivers messages over a Go channel for idiomatic consumption

```
Your Go app  -->  claude-agent-sdk-go  -->  claude CLI  -->  Anthropic API
                  (this library)            (subprocess)
```

## Forward Compatibility

The SDK is designed for forward compatibility with future CLI versions:

- **Unknown message types** are returned as `*UnknownMessage` with the raw JSON preserved, never as errors.
- **Unknown content block types** within messages are silently skipped.

This ensures your application won't break when the CLI adds new message or content types.

## License

[MIT](LICENSE)
