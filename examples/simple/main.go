// Command simple demonstrates a basic one-shot query using the Claude Agent SDK.
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
