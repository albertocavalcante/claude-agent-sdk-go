package claude

import (
	"slices"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
)

// Launch is everything needed to start the CLI the way this SDK would.
type Launch struct {
	// Path is the executable name or path (not resolved).
	Path string
	// Args is the argv after the executable, CLIPrefixArgs included.
	Args []string
	// Env is the complete child environment with the safe defaults applied:
	// CLAUDECODE and EnvUnset keys removed, CLAUDE_CODE_ENTRYPOINT,
	// CLAUDE_AGENT_SDK_VERSION and CLAUDE_CODE_SDK_READS_SESSION_STATE set.
	Env []string
	// Dir is the working directory.
	Dir string
	// Cleanup removes the generated MCP config file. Idempotent; never nil.
	Cleanup func()
}

// LaunchSpec renders opts into a Launch without starting anything. A
// transport that starts the CLI somewhere else (a runner process that owns
// the pipes) must launch from it, so the security defaults (user-only setting
// sources, scrubbed environment, per-client 0600 MCP config) apply there too.
// The MCP config file is written where LaunchSpec runs; a remote runner must
// copy it or use inline equivalents. Env: nil Options.Env inherits this
// process's environment.
func LaunchSpec(opts Options) (Launch, error) {
	l := Launch{Cleanup: func() {}}
	mcpPath := ""
	if len(opts.MCPServers) > 0 {
		p, cleanup, err := config.WriteMCPConfig("", opts.MCPServers)
		if err != nil {
			return l, err
		}
		mcpPath, l.Cleanup = p, cleanup
	}
	args, err := buildArgs(&opts, mcpPath)
	if err != nil {
		l.Cleanup()
		return Launch{Cleanup: func() {}}, err
	}
	l.Path = opts.CLIPath
	if l.Path == "" {
		l.Path = "claude"
	}
	l.Args = append(append([]string(nil), opts.CLIPrefixArgs...), args...)
	l.Dir = opts.Cwd
	l.Env = transport.BuildEnv(opts.Env, opts.EnvUnset, opts.Cwd)
	if opts.EnableFileCheckpointing && !slices.Contains(opts.EnvUnset, "CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING") {
		l.Env = append(l.Env, "CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING=true")
	}
	return l, nil
}
