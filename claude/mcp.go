package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MCPServerConfig defines an MCP server that the Claude CLI should connect to.
type MCPServerConfig struct {
	// Name is a human-readable identifier for this server.
	Name string

	// Command is the executable to run for this MCP server.
	Command string

	// Args are command-line arguments passed to the MCP server.
	Args []string

	// Env provides environment variables for the MCP server process.
	Env map[string]string

	// CWD sets the working directory for the MCP server process.
	CWD string
}

// MCPTool defines a tool that an MCP server exposes.
// This is used for documentation and type safety — the actual tool
// implementation lives in the MCP server.
type MCPTool struct {
	// Name is the tool name as exposed by the MCP server.
	Name string

	// Description explains what the tool does.
	Description string

	// InputSchema is the JSON Schema for the tool's input.
	InputSchema map[string]any
}

// mcpConfigFile is the JSON structure expected by --mcp-config.
type mcpConfigFile struct {
	MCPServers map[string]mcpServerEntry `json:"mcpServers"`
}

type mcpServerEntry struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
}

// WriteMCPConfig writes the MCP server configurations to a temporary JSON file
// suitable for passing to the Claude CLI's --mcp-config flag.
// The caller is responsible for cleaning up the file when done.
func WriteMCPConfig(servers []MCPServerConfig) (string, error) {
	if len(servers) == 0 {
		return "", fmt.Errorf("no MCP servers provided")
	}

	config := mcpConfigFile{
		MCPServers: make(map[string]mcpServerEntry, len(servers)),
	}

	for _, s := range servers {
		if s.Name == "" {
			return "", fmt.Errorf("MCP server name is required")
		}
		if s.Command == "" {
			return "", fmt.Errorf("MCP server %q: command is required", s.Name)
		}
		config.MCPServers[s.Name] = mcpServerEntry{
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
			CWD:     s.CWD,
		}
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshaling MCP config: %w", err)
	}

	// Write to a temp file.
	tmpDir := os.TempDir()
	configPath := filepath.Join(tmpDir, fmt.Sprintf("claude-mcp-%d.json", os.Getpid()))
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return "", fmt.Errorf("writing MCP config: %w", err)
	}

	return configPath, nil
}
