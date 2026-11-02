package claude

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/protocol"
	"github.com/albertocavalcante/claude-agent-sdk-go/claude/transport"
)

// PermissionMode is a CLI permission mode.
type PermissionMode string

// Permission modes of CLI 2.1.x. The CLI's --help omits "default" but both
// official SDKs send it; "auto" and "manual" appear in --help.
const (
	ModeDefault           PermissionMode = "default"
	ModeAcceptEdits       PermissionMode = "acceptEdits"
	ModeBypassPermissions PermissionMode = "bypassPermissions"
	ModePlan              PermissionMode = "plan"
	ModeDontAsk           PermissionMode = "dontAsk"
	ModeAuto              PermissionMode = "auto"
	ModeManual            PermissionMode = "manual"
)

func (m PermissionMode) valid() bool {
	switch m {
	case "", ModeDefault, ModeAcceptEdits, ModeBypassPermissions, ModePlan, ModeDontAsk, ModeAuto, ModeManual:
		return true
	}
	return false
}

// Ptr returns a pointer to v, for the *bool and similar optional fields of Options.
func Ptr[T any](v T) *T { return &v }

// Options configures a Client. The zero value launches `claude` from PATH
// with safe defaults: only the user's own settings are loaded
// (SettingSources nil means user), and user messages are replayed.
type Options struct {
	// CLIPath is the claude executable (default "claude" on PATH).
	CLIPath string
	// CLIPrefixArgs are inserted before the generated flags (tests, wrappers).
	CLIPrefixArgs []string
	// Cwd is the working directory of the CLI.
	Cwd string

	Model         string
	FallbackModel string
	// PermissionMode is the starting permission mode.
	PermissionMode PermissionMode
	// SystemPrompt replaces the default prompt (--system-prompt). Empty keeps
	// the Claude Code preset, unlike the official SDKs, which default to an
	// empty prompt.
	SystemPrompt string
	// AppendSystemPrompt is appended to the prompt (--append-system-prompt).
	AppendSystemPrompt string

	// Tools restricts the built-in tool set (--tools). nil keeps the default;
	// an empty non-nil slice disables all built-in tools (--tools "").
	Tools []string
	// AllowedTools and DisallowedTools omit their flag when empty.
	AllowedTools      []string
	DisallowedTools   []string
	MaxTurns          int // per user turn, not per process
	MaxThinkingTokens int
	AddDirs           []string

	// SettingSources selects settings sources. nil means user only: the
	// default sources would run a repository's hooks, .mcp.json servers, env
	// and apiKeyHelper, and a repo env.ANTHROPIC_BASE_URL leaks the OAuth
	// bearer token. A non-nil empty slice loads none. To use a project's
	// settings safely pass the output of config.FilteredProjectSettings as
	// Settings instead of adding config.SourceProject.
	SettingSources []config.SettingSource
	// Settings is a settings file path passed as --settings.
	Settings string
	// MCPServers are written to a per-client 0600 file and passed as --mcp-config.
	MCPServers map[string]config.MCPServer
	// StrictMCPConfig ignores every MCP source except MCPServers.
	StrictMCPConfig bool

	// Resume continues the session with this id (same id back). Continue
	// resumes the most recent session in Cwd. ForkSession makes the resumed
	// session a copy with a new id. SessionID names a new session.
	Resume      string
	Continue    bool
	ForkSession bool
	SessionID   string

	// IncludePartialMessages enables stream_event frames.
	IncludePartialMessages bool
	// ReplayUserMessages echoes each user message on stdout with its uuid.
	// nil means true.
	ReplayUserMessages *bool
	// EnableFileCheckpointing sets CLAUDE_CODE_ENABLE_SDK_FILE_CHECKPOINTING
	// so RewindFiles works.
	EnableFileCheckpointing bool

	// Env is the base environment (KEY=VALUE); nil inherits os.Environ().
	// EnvUnset removes keys, e.g. CLAUDE_CODE_OAUTH_TOKEN. CLAUDECODE is
	// always removed.
	Env      []string
	EnvUnset []string

	// CanUseTool answers tool permission requests. Setting it launches the CLI
	// with --permission-prompt-tool stdio. It runs on its own goroutine with a
	// context cancelled by control_cancel_request, Close and Detach.
	CanUseTool func(ctx context.Context, req ToolPermissionRequest) (PermissionDecision, error)
	// Hooks registers hook callbacks at initialize.
	Hooks map[HookEvent][]HookMatcher
	// OnElicitation answers MCP elicitation requests; nil declines them.
	OnElicitation func(ctx context.Context, req ElicitationRequest) (ElicitationResult, error)

	// InitializeTimeout bounds the initialize handshake (default 60s).
	InitializeTimeout time.Duration
	// MessageBuffer is the capacity of the Messages queue (default 256).
	// When it is full the reader blocks until a consumer drains it; control
	// responses and callbacks are then stalled too, so keep consuming
	// Messages while a turn runs.
	MessageBuffer int
	// Limits caps line size (default 16 MiB) and JSON depth (default 256).
	Limits protocol.Limits

	// OnReplyError is called when a reply to an inbound request could not be
	// written (the CLI then still waits for it; Reinitialize resends it).
	OnReplyError func(requestID string, err error)

	// Transport overrides the subprocess (tests, remote hosts).
	Transport transport.Transport
	// Stderr receives each CLI stderr line (Subprocess only). The last 64 KiB
	// are also kept for errors.
	Stderr func(line string)
	// ExtraArgs are appended last as "--key value" (an empty value is passed
	// as an empty argument, "--key=value" if value starts with "-"). Escape
	// hatch for new flags. ExtraFlags are bare flags without a value.
	ExtraArgs  map[string]string
	ExtraFlags []string
}

// ErrInvalidOptions wraps option validation failures.
var ErrInvalidOptions = errors.New("claude: invalid options")

func invalid(format string, a ...any) error {
	return fmt.Errorf("claude: invalid options: "+format+": %w", append(a, ErrInvalidOptions)...)
}

// buildArgs renders the argv (without the executable) for opts. mcpPath is
// the generated MCP config file, if any.
func buildArgs(o *Options, mcpPath string) ([]string, error) {
	switch {
	case !o.PermissionMode.valid():
		return nil, invalid("permission mode %q", string(o.PermissionMode))
	case o.ForkSession && o.Resume == "" && !o.Continue:
		return nil, invalid("ForkSession needs Resume or Continue")
	case o.Resume != "" && o.Continue:
		return nil, invalid("Resume and Continue are mutually exclusive")
	case o.SessionID != "" && (o.Resume != "" || o.Continue) && !o.ForkSession:
		return nil, invalid("SessionID with Resume needs ForkSession")
	case o.MaxTurns < 0 || o.MaxThinkingTokens < 0:
		return nil, invalid("negative limit")
	}
	for _, group := range [][]string{o.AllowedTools, o.DisallowedTools, o.Tools} {
		for _, tool := range group {
			if strings.Contains(tool, ",") {
				// the CLI splits the tool list on commas, so an entry holding one is ambiguous
				return nil, invalid("tool entry %q contains a comma", tool)
			}
		}
	}
	args := []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}
	add := func(a ...string) { args = append(args, a...) }
	// Value flags use the --flag=value form so a value that starts with '-'
	// can never be read as another flag.
	kv := func(flag, v string) { add(flag + "=" + v) }
	if o.CanUseTool != nil {
		kv("--permission-prompt-tool", "stdio")
	}
	if o.Model != "" {
		kv("--model", o.Model)
	}
	if o.FallbackModel != "" {
		kv("--fallback-model", o.FallbackModel)
	}
	if o.PermissionMode != "" {
		kv("--permission-mode", string(o.PermissionMode))
	}
	if o.SystemPrompt != "" {
		kv("--system-prompt", o.SystemPrompt)
	}
	if o.AppendSystemPrompt != "" {
		kv("--append-system-prompt", o.AppendSystemPrompt)
	}
	if o.Tools != nil {
		if len(o.Tools) == 0 {
			add("--tools", "") // no built-in tools: the proven separate empty argument
		} else {
			kv("--tools", strings.Join(o.Tools, ","))
		}
	}
	if len(o.AllowedTools) > 0 {
		kv("--allowedTools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		kv("--disallowedTools", strings.Join(o.DisallowedTools, ","))
	}
	if o.MaxTurns > 0 {
		kv("--max-turns", strconv.Itoa(o.MaxTurns))
	}
	if o.MaxThinkingTokens > 0 {
		kv("--max-thinking-tokens", strconv.Itoa(o.MaxThinkingTokens))
	}
	for _, d := range o.AddDirs {
		kv("--add-dir", d)
	}
	if o.Settings != "" {
		kv("--settings", o.Settings)
	}
	if mcpPath != "" {
		kv("--mcp-config", mcpPath)
	}
	if o.StrictMCPConfig {
		add("--strict-mcp-config")
	}
	if o.Resume != "" {
		kv("--resume", o.Resume)
	}
	if o.Continue {
		add("--continue")
	}
	if o.ForkSession {
		add("--fork-session")
	}
	if o.SessionID != "" {
		kv("--session-id", o.SessionID)
	}
	if o.IncludePartialMessages {
		add("--include-partial-messages")
	}
	add(config.SettingSourcesArgs(o.SettingSources)...)
	if o.ReplayUserMessages == nil || *o.ReplayUserMessages {
		add("--replay-user-messages")
	}
	keys := make([]string, 0, len(o.ExtraArgs))
	for k := range o.ExtraArgs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		flag, v := "--"+strings.TrimLeft(k, "-"), o.ExtraArgs[k]
		if v == "" {
			add(flag, "")
		} else {
			kv(flag, v)
		}
	}
	for _, f := range o.ExtraFlags {
		add("--" + strings.TrimLeft(f, "-"))
	}
	return args, nil
}
