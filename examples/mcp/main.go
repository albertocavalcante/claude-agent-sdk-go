// Command mcp demonstrates using external MCP tools with the Claude Agent SDK.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude"
)

func main() {
	client := claude.NewClient(claude.Options{
		Model: "sonnet-4-6",
		MCPServers: []claude.MCPServerConfig{
			{
				Name:    "filesystem",
				Command: "npx",
				Args:    []string{"-y", "@modelcontextprotocol/server-filesystem", "/tmp"},
			},
		},
		Hooks: []claude.HookRegistration{
			{
				Event: claude.HookPreToolUse,
				Callback: func(ctx context.Context, event claude.HookInput) (claude.HookOutput, error) {
					fmt.Printf("[mcp] Tool: %s\n", event.ToolName)
					return claude.HookOutput{}, nil
				},
			},
		},
	})

	ctx := context.Background()
	prompt := "List the files in /tmp using the filesystem tools"
	if len(os.Args) > 1 {
		prompt = os.Args[1]
	}

	for msg := range client.Query(ctx, prompt) {
		if msg.Err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", msg.Err)
			os.Exit(1)
		}
		if am, ok := msg.Message.(*claude.AssistantMessage); ok {
			for _, block := range am.Content {
				if tb, ok := block.(*claude.TextBlock); ok {
					fmt.Print(tb.Text)
				}
			}
		}
	}
	fmt.Println()
}
