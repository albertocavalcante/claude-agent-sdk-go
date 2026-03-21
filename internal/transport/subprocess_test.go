package transport

import (
	"testing"
)

func TestBuildArgsMinimal(t *testing.T) {
	args := buildArgs("hello", nil)
	// Should contain: --print, --output-format, stream-json, --verbose, -p, hello
	expected := []string{"--print", "--output-format", "stream-json", "--verbose", "-p", "hello"}
	if len(args) != len(expected) {
		t.Fatalf("expected %d args, got %d: %v", len(expected), len(args), args)
	}
	for i, want := range expected {
		if args[i] != want {
			t.Errorf("args[%d] = %q, want %q", i, args[i], want)
		}
	}
}

func TestBuildArgsWithModel(t *testing.T) {
	args := buildArgs("test", &Options{Model: "sonnet-4-6"})
	assertContains(t, args, "--model", "sonnet-4-6")
}

func TestBuildArgsWithSystemPrompt(t *testing.T) {
	args := buildArgs("test", &Options{SystemPrompt: "You are a helper"})
	assertContains(t, args, "--system-prompt", "You are a helper")
}

func TestBuildArgsWithAppendSystemPrompt(t *testing.T) {
	args := buildArgs("test", &Options{AppendSystemPrompt: "Be concise"})
	assertContains(t, args, "--append-system-prompt", "Be concise")
}

func TestBuildArgsWithMaxThinkingTokens(t *testing.T) {
	args := buildArgs("test", &Options{MaxThinkingTokens: 1000})
	assertContains(t, args, "--max-thinking-tokens", "1000")
}

func TestBuildArgsWithPermissionMode(t *testing.T) {
	args := buildArgs("test", &Options{PermissionMode: "bypassPermissions"})
	assertContains(t, args, "--permission-mode", "bypassPermissions")
}

func TestBuildArgsWithAllowedTools(t *testing.T) {
	args := buildArgs("test", &Options{AllowedTools: []string{"Bash", "Read"}})
	assertContains(t, args, "--allowedTools", "Bash")
	assertContains(t, args, "--allowedTools", "Read")
}

func TestBuildArgsWithDisallowedTools(t *testing.T) {
	args := buildArgs("test", &Options{DisallowedTools: []string{"Write"}})
	assertContains(t, args, "--disallowedTools", "Write")
}

func TestBuildArgsWithMaxTurns(t *testing.T) {
	args := buildArgs("test", &Options{MaxTurns: 5})
	assertContains(t, args, "--max-turns", "5")
}

func TestBuildArgsFullOptions(t *testing.T) {
	args := buildArgs("complex prompt", &Options{
		Model:             "opus-4-6",
		SystemPrompt:      "Be helpful",
		MaxThinkingTokens: 2000,
		PermissionMode:    "acceptEdits",
		AllowedTools:      []string{"Bash"},
		MaxTurns:          10,
	})

	// Verify all flags are present
	assertContains(t, args, "--model", "opus-4-6")
	assertContains(t, args, "--system-prompt", "Be helpful")
	assertContains(t, args, "--max-thinking-tokens", "2000")
	assertContains(t, args, "--permission-mode", "acceptEdits")
	assertContains(t, args, "--allowedTools", "Bash")
	assertContains(t, args, "--max-turns", "10")
	assertContains(t, args, "-p", "complex prompt")
}

func TestBuildArgsPromptAlwaysLast(t *testing.T) {
	args := buildArgs("my prompt", &Options{Model: "haiku"})
	// -p and prompt should be the last two args
	if len(args) < 2 {
		t.Fatal("expected at least 2 args")
	}
	if args[len(args)-2] != "-p" {
		t.Errorf("expected -p as second-to-last arg, got %q", args[len(args)-2])
	}
	if args[len(args)-1] != "my prompt" {
		t.Errorf("expected prompt as last arg, got %q", args[len(args)-1])
	}
}

func TestBuildArgsWithSessionID(t *testing.T) {
	args := buildArgs("test", &Options{SessionID: "sess_123"})
	assertContains(t, args, "--session-id", "sess_123")
}

func TestBuildArgsWithMCPConfig(t *testing.T) {
	args := buildArgs("test", &Options{MCPConfigPath: "/tmp/mcp.json"})
	assertContains(t, args, "--mcp-config", "/tmp/mcp.json")
}

func TestSubprocessTransportCloseWithoutStart(t *testing.T) {
	st := &SubprocessTransport{}
	// Close on unstarted transport should not panic
	err := st.Close()
	if err != nil {
		t.Errorf("Close on unstarted transport should not error, got: %v", err)
	}
}

func TestSubprocessTransportLinesNil(t *testing.T) {
	st := &SubprocessTransport{}
	ch := st.Lines()
	if ch != nil {
		t.Error("Lines() on unstarted transport should return nil")
	}
}

// assertContains checks that args contains the given flag-value pair.
func assertContains(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, arg := range args {
		if arg == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("args %v does not contain %s %s", args, flag, value)
}
