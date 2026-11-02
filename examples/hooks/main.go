// Command hooks shows the two callback kinds of a long-lived Client: a
// permission callback (CanUseTool) that approves or denies tool calls, and a
// PreToolUse hook callback that observes them. Both run on their own
// goroutines while Turn keeps draining messages.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	client, err := claude.NewClient(ctx, claude.Options{
		Model:        "haiku",
		AllowedTools: []string{"Read"},
		// Ask before every Bash call that is not read-only (the CLI itself
		// auto-approves read-only commands and never asks about those).
		CanUseTool: func(_ context.Context, req claude.ToolPermissionRequest) (claude.PermissionDecision, error) {
			fmt.Printf("[permission] %s %s\n", req.ToolName, req.Input)
			if strings.Contains(string(req.Input), "rm ") {
				return claude.Deny{Message: "destructive commands are not allowed here"}, nil
			}
			return claude.Allow{}, nil // the original input is sent back unchanged
		},
		Hooks: map[claude.HookEvent][]claude.HookMatcher{
			claude.HookPreToolUse: {{
				Matcher: "Bash|Read",
				Callbacks: []claude.HookFunc{
					func(_ context.Context, in claude.HookInput) (claude.HookOutput, error) {
						fmt.Printf("[hook] about to run %s\n", in.ToolName)
						return claude.HookOutput{}, nil
					},
				},
			}},
		},
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }() // denies any open approval, then ends the process

	res, err := client.Turn(ctx, claude.UserText("Run `ls` with the Bash tool and tell me how many entries there are."))
	if err != nil {
		return err
	}
	fmt.Println(res.Result)
	return nil
}
