package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
)

// MCP server types.
const (
	MCPStdio = "stdio"
	MCPHTTP  = "http"
	MCPSSE   = "sse"
)

// MCPServer is one entry of an MCP config. Type defaults to stdio when
// Command is set.
type MCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func (s MCPServer) validated(name string) (MCPServer, error) {
	if name == "" {
		return s, errors.New("config: MCP server with empty name")
	}
	if s.Type == "" && s.Command != "" {
		s.Type = MCPStdio
	}
	switch s.Type {
	case MCPStdio:
		if s.Command == "" {
			return s, fmt.Errorf("config: MCP server %q: stdio needs a command", name)
		}
	case MCPHTTP, MCPSSE:
		if s.URL == "" {
			return s, fmt.Errorf("config: MCP server %q: %s needs a url", name, s.Type)
		}
	case "":
		return s, fmt.Errorf("config: MCP server %q: no type and no command", name)
	default:
		return s, fmt.Errorf("config: MCP server %q: unknown type %q", name, s.Type)
	}
	return s, nil
}

// WriteMCPConfig writes servers as a --mcp-config file. The file is created
// with a unique name (os.CreateTemp, so two clients never collide) and mode
// 0600 because it can hold tokens in headers and env. dir "" uses the OS temp
// dir. cleanup is idempotent.
func WriteMCPConfig(dir string, servers map[string]MCPServer) (path string, cleanup func(), err error) {
	out := make(map[string]MCPServer, len(servers))
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		v, err := servers[n].validated(n)
		if err != nil {
			return "", nil, err
		}
		out[n] = v
	}
	data, err := json.MarshalIndent(struct {
		MCPServers map[string]MCPServer `json:"mcpServers"`
	}{out}, "", "  ")
	if err != nil {
		return "", nil, err
	}
	return writeTemp(dir, "claude-mcp-*.json", data)
}

func writeTemp(dir, pattern string, data []byte) (string, func(), error) {
	if dir == "" {
		d, err := RunDir()
		if err != nil {
			return "", nil, err
		}
		dir = d
	}
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) } // only the file: the run dir is shared and swept later
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		cleanup()
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return name, cleanup, nil
}
