package claude

// Options configures a Claude CLI invocation.
type Options struct {
	// Model specifies the Claude model to use (e.g. "opus-4-6", "sonnet-4-6", "haiku").
	Model string

	// SystemPrompt overrides the default system prompt entirely.
	SystemPrompt string

	// AppendSystemPrompt appends text to the default system prompt.
	AppendSystemPrompt string

	// AllowedTools is a whitelist of tool names the model may invoke.
	AllowedTools []string

	// DisallowedTools is a blacklist of tool names the model may not invoke.
	DisallowedTools []string

	// MaxThinkingTokens sets the extended thinking token budget.
	MaxThinkingTokens int

	// MaxTurns limits the number of agent loop turns.
	MaxTurns int

	// WorkingDirectory sets the working directory for the CLI subprocess.
	WorkingDirectory string

	// PermissionMode controls tool permission behavior.
	// Valid values: "default", "acceptEdits", "bypassPermissions".
	PermissionMode string

	// CLIPath overrides the path to the claude binary.
	// If empty, "claude" is resolved from PATH.
	CLIPath string

	// CLIPrefixArgs are arguments inserted between CLIPath and the SDK-generated flags.
	// This is useful for invoking the CLI via a package runner like npx:
	//   CLIPath: "npx", CLIPrefixArgs: []string{"--yes", "@anthropic-ai/claude-code"}
	CLIPrefixArgs []string

	// Env provides additional environment variables for the CLI subprocess.
	Env map[string]string

	// Hooks registers callbacks for lifecycle events.
	// Used by ClaudeClient to fire callbacks during message processing.
	Hooks []HookRegistration

	// SessionID resumes a previous session. If empty, a new session is created.
	SessionID string

	// MCPServers configures external MCP servers for the Claude CLI.
	// Each server exposes tools that Claude can use during conversation.
	// If both MCPServers and MCPConfigPath are set, MCPConfigPath takes
	// precedence and MCPServers is ignored.
	MCPServers []MCPServerConfig

	// MCPConfigPath is an optional path to a pre-existing MCP config JSON file.
	// When set, the SDK uses this file directly instead of auto-generating one
	// from MCPServers. The caller is responsible for managing the file's lifecycle.
	MCPConfigPath string
}
