package claude

import (
	"encoding/json"
	"os"
	"testing"
)

func TestWriteMCPConfig(t *testing.T) {
	servers := []MCPServerConfig{
		{
			Name:    "my-tools",
			Command: "node",
			Args:    []string{"server.js"},
			Env:     map[string]string{"PORT": "3000"},
		},
	}

	path, err := WriteMCPConfig(servers)
	if err != nil {
		t.Fatalf("WriteMCPConfig failed: %v", err)
	}
	defer os.Remove(path)

	// Read and parse the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	var config mcpConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	if len(config.MCPServers) != 1 {
		t.Fatalf("expected 1 server, got %d", len(config.MCPServers))
	}

	server, ok := config.MCPServers["my-tools"]
	if !ok {
		t.Fatal("server 'my-tools' not found in config")
	}
	if server.Command != "node" {
		t.Errorf("expected command 'node', got %q", server.Command)
	}
	if len(server.Args) != 1 || server.Args[0] != "server.js" {
		t.Errorf("expected args ['server.js'], got %v", server.Args)
	}
	if server.Env["PORT"] != "3000" {
		t.Errorf("expected env PORT=3000, got %v", server.Env)
	}
}

func TestWriteMCPConfigMultipleServers(t *testing.T) {
	servers := []MCPServerConfig{
		{Name: "server-a", Command: "python3", Args: []string{"a.py"}},
		{Name: "server-b", Command: "node", Args: []string{"b.js"}},
	}

	path, err := WriteMCPConfig(servers)
	if err != nil {
		t.Fatalf("WriteMCPConfig failed: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	var config mcpConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	if len(config.MCPServers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(config.MCPServers))
	}
}

func TestWriteMCPConfigEmptyServers(t *testing.T) {
	_, err := WriteMCPConfig(nil)
	if err == nil {
		t.Fatal("expected error for empty servers")
	}
}

func TestWriteMCPConfigMissingName(t *testing.T) {
	servers := []MCPServerConfig{
		{Command: "node"},
	}
	_, err := WriteMCPConfig(servers)
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestWriteMCPConfigMissingCommand(t *testing.T) {
	servers := []MCPServerConfig{
		{Name: "test"},
	}
	_, err := WriteMCPConfig(servers)
	if err == nil {
		t.Fatal("expected error for missing command")
	}
}

func TestWriteMCPConfigWithCWD(t *testing.T) {
	servers := []MCPServerConfig{
		{
			Name:    "tools",
			Command: "python3",
			Args:    []string{"server.py"},
			CWD:     "/tmp/tools",
		},
	}

	path, err := WriteMCPConfig(servers)
	if err != nil {
		t.Fatalf("WriteMCPConfig failed: %v", err)
	}
	defer os.Remove(path)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read config: %v", err)
	}

	var config mcpConfigFile
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("failed to parse config: %v", err)
	}

	server := config.MCPServers["tools"]
	if server.CWD != "/tmp/tools" {
		t.Errorf("expected CWD '/tmp/tools', got %q", server.CWD)
	}
}

func TestCleanupMCPConfig(t *testing.T) {
	// Create a temp file to clean up.
	servers := []MCPServerConfig{
		{Name: "test", Command: "echo"},
	}
	path, err := WriteMCPConfig(servers)
	if err != nil {
		t.Fatalf("WriteMCPConfig failed: %v", err)
	}

	// File should exist.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("config file should exist: %v", err)
	}

	// Clean up.
	if err := CleanupMCPConfig(path); err != nil {
		t.Fatalf("CleanupMCPConfig failed: %v", err)
	}

	// File should be gone.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("config file should not exist after cleanup")
	}
}

func TestCleanupMCPConfigEmptyPath(t *testing.T) {
	// Should be a no-op.
	if err := CleanupMCPConfig(""); err != nil {
		t.Errorf("CleanupMCPConfig with empty path should not error: %v", err)
	}
}

func TestMCPConfigPathPrecedence(t *testing.T) {
	// When MCPConfigPath is set, MCPServers should be ignored.
	opts := Options{
		MCPConfigPath: "/tmp/my-existing-config.json",
		MCPServers: []MCPServerConfig{
			{Name: "ignored", Command: "should-not-be-written"},
		},
	}

	tOpts, cleanup := toTransportOptions(&opts)
	defer cleanup()

	if tOpts.MCPConfigPath != "/tmp/my-existing-config.json" {
		t.Errorf("expected MCPConfigPath '/tmp/my-existing-config.json', got %q", tOpts.MCPConfigPath)
	}
}

func TestWriteMCPConfigFilePermissions(t *testing.T) {
	servers := []MCPServerConfig{
		{Name: "test", Command: "echo"},
	}

	path, err := WriteMCPConfig(servers)
	if err != nil {
		t.Fatalf("WriteMCPConfig failed: %v", err)
	}
	defer os.Remove(path)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("failed to stat config: %v", err)
	}

	// Should be 0600 (owner read/write only).
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("expected permissions 0600, got %o", perm)
	}
}
