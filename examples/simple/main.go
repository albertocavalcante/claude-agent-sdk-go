// Command simple runs a one-shot prompt and prints the answer.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
)

func main() {
	ctx := context.Background()
	for msg, err := range claude.Query(ctx, "What is 2+2? Reply in one word.", claude.Options{Model: "haiku"}) {
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		switch m := msg.(type) {
		case *protocol.Assistant:
			fmt.Print(m.Text())
		case *protocol.Result:
			fmt.Printf("\n---\nturns: %d, cost: $%.4f\n", m.NumTurns, m.TotalCostUSD)
		}
	}
}
