// Command mcp gives Claude an MCP server. The servers are written to a
// per-client 0600 file and passed with --mcp-config.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
)

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	client, err := claude.NewClient(ctx, claude.Options{
		Model: "haiku",
		MCPServers: map[string]config.MCPServer{
			// stdio server; http and sse servers use Type, URL and Headers
			"filesystem": {Command: "npx", Args: []string{"-y", "@modelcontextprotocol/server-filesystem", "."}},
		},
		StrictMCPConfig: true, // ignore every other MCP source
	})
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	status, err := client.MCPStatus(ctx)
	if err != nil {
		return err
	}
	for i := range status.MCPServers {
		fmt.Printf("%s: %s\n", status.MCPServers[i].Name, status.MCPServers[i].Status)
	}
	return nil
}
