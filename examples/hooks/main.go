// Command hooks demonstrates lifecycle hook usage with the Claude Agent SDK.
//
// It registers hooks for tool use events and result events, printing
// informational messages as the agent executes tools.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude"
)

func main() {
	client := claude.NewClient(claude.Options{
		Model: "haiku",
		Hooks: []claude.HookRegistration{
			{
				Event: claude.HookPreToolUse,
				Callback: func(ctx context.Context, event claude.HookInput) (claude.HookOutput, error) {
					fmt.Printf("[hook] Tool call: %s\n", event.ToolName)
					return claude.HookOutput{}, nil
				},
			},
			{
				Event: claude.HookPostToolUse,
				Callback: func(ctx context.Context, event claude.HookInput) (claude.HookOutput, error) {
					fmt.Printf("[hook] Tool result for %s: %d bytes\n", event.ToolName, len(event.ToolOutput))
					return claude.HookOutput{}, nil
				},
			},
			{
				Event: claude.HookResult,
				Callback: func(ctx context.Context, event claude.HookInput) (claude.HookOutput, error) {
					if rm, ok := event.Message.(*claude.ResultMessage); ok {
						fmt.Printf("[hook] Done! Cost: $%.4f, Tokens: %d in / %d out\n",
							rm.Cost, rm.InputTokens, rm.OutputTokens)
					}
					return claude.HookOutput{}, nil
				},
			},
		},
	})

	ctx := context.Background()
	prompt := "List the files in the current directory"
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
