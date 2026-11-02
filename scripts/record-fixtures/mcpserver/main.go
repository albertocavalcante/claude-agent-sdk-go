// Command mcpserver is a minimal stdio MCP server with one tool, "echo". The
// fixture recorder runs the CLI against it so recordings contain a tool
// result from a neutral local server instead of anyone's real connectors.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

type req struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params struct {
		Name      string `json:"name"`
		Arguments struct {
			Text string `json:"text"`
		} `json:"arguments"`
	} `json:"params"`
}

func main() {
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		var r req
		if json.Unmarshal(sc.Bytes(), &r) != nil || len(r.ID) == 0 {
			continue // notifications and garbage need no reply
		}
		var result any
		switch r.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "example", "version": "0.0.1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{
				"name": "echo", "description": "Echo the given text back.",
				"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}},
			}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + r.Params.Arguments.Text}}}
		default:
			_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			continue
		}
		if err := out.Encode(map[string]any{"jsonrpc": "2.0", "id": r.ID, "result": result}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return
		}
	}
}
