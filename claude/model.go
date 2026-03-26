package claude

// Model constants for commonly used Claude models.
const (
	ModelOpus   = "claude-opus-4-6"
	ModelSonnet = "claude-sonnet-4-6"
	ModelHaiku  = "claude-haiku-4-5"
)

// PermissionMode constants control tool permission behavior.
const (
	PermissionDefault           = "default"
	PermissionAcceptEdits       = "acceptEdits"
	PermissionBypassPermissions = "bypassPermissions"
)

// Exit code constants returned by the Claude CLI.
const (
	ExitSuccess      = 0
	ExitError        = 1
	ExitInvalidInput = 2
	ExitTurnLimit    = 3
)
