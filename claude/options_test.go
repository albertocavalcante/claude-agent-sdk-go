package claude

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/albertocavalcante/claude-agent-sdk-go/claude/config"
)

var baseArgs = []string{"--output-format", "stream-json", "--verbose", "--input-format", "stream-json"}

// defaultTail is what every launch carries: user-only settings and replay.
var defaultTail = []string{"--setting-sources=user", "--replay-user-messages"}

func want(extra ...string) []string {
	out := append([]string(nil), baseArgs...)
	out = append(out, extra...)
	return append(out, defaultTail...)
}

func TestBuildArgs(t *testing.T) {
	no := false
	cases := []struct {
		name string
		opts Options
		want []string
	}{
		{"defaults", Options{}, want()},
		{"model", Options{Model: "haiku"}, want("--model=haiku")},
		{"fallback model", Options{FallbackModel: "sonnet"}, want("--fallback-model=sonnet")},
		{"permission mode plan", Options{PermissionMode: ModePlan}, want("--permission-mode=plan")},
		{"permission mode acceptEdits", Options{PermissionMode: ModeAcceptEdits}, want("--permission-mode=acceptEdits")},
		{"permission mode bypass", Options{PermissionMode: ModeBypassPermissions}, want("--permission-mode=bypassPermissions")},
		{"permission mode dontAsk", Options{PermissionMode: ModeDontAsk}, want("--permission-mode=dontAsk")},
		{"permission mode default", Options{PermissionMode: ModeDefault}, want("--permission-mode=default")},
		{"system prompt", Options{SystemPrompt: "be brief"}, want("--system-prompt=be brief")},
		{"append system prompt", Options{AppendSystemPrompt: "also"}, want("--append-system-prompt=also")},
		{"allowed tools", Options{AllowedTools: []string{"Read", "Bash(git *)"}}, want("--allowedTools=Read,Bash(git *)")},
		{"disallowed tools", Options{DisallowedTools: []string{"Bash"}}, want("--disallowedTools=Bash")},
		{"tools nil", Options{}, want()},
		{"tools empty", Options{Tools: []string{}}, want("--tools", "")},
		{"tools list", Options{Tools: []string{"Bash", "Read"}}, want("--tools=Bash,Read")},
		{"allowed tools empty is omitted", Options{AllowedTools: []string{}}, want()},
		{"disallowed tools empty is omitted", Options{DisallowedTools: []string{}}, want()},
		{"max turns", Options{MaxTurns: 3}, want("--max-turns=3")},
		{"max thinking tokens", Options{MaxThinkingTokens: 2048}, want("--max-thinking-tokens=2048")},
		{"add dirs", Options{AddDirs: []string{"/a", "/b"}}, want("--add-dir=/a", "--add-dir=/b")},
		{"project sources", Options{SettingSources: []config.SettingSource{config.SourceUser, config.SourceProject}},
			append(append([]string(nil), baseArgs...), "--setting-sources=user,project", "--replay-user-messages")},
		{"no sources", Options{SettingSources: []config.SettingSource{}},
			append(append([]string(nil), baseArgs...), "--setting-sources=", "--replay-user-messages")},
		{"settings file", Options{Settings: "/tmp/s.json"}, want("--settings=/tmp/s.json")},
		{"strict mcp", Options{StrictMCPConfig: true}, want("--strict-mcp-config")},
		{"resume", Options{Resume: "abc"}, want("--resume=abc")},
		{"resume with dash", Options{Resume: "-x"}, want("--resume=-x")},
		{"continue", Options{Continue: true}, want("--continue")},
		{"fork", Options{Resume: "abc", ForkSession: true}, want("--resume=abc", "--fork-session")},
		{"session id", Options{SessionID: "11111111-1111-4111-8111-111111111111"}, want("--session-id=11111111-1111-4111-8111-111111111111")},
		{"partial messages", Options{IncludePartialMessages: true}, want("--include-partial-messages")},
		{"replay off", Options{ReplayUserMessages: &no},
			append(append([]string(nil), baseArgs...), "--setting-sources=user")},
		{"can use tool", Options{CanUseTool: func(_ context.Context, _ ToolPermissionRequest) (PermissionDecision, error) { return Deny{}, nil }},
			want("--permission-prompt-tool=stdio")},
		{"extra args", Options{ExtraArgs: map[string]string{"effort": "low", "empty": "", "b": "-v"}, ExtraFlags: []string{"debug"}},
			append(want(), "--b=-v", "--effort=low", "--empty", "", "--debug")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := buildArgs(&c.opts, "")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("\n got %q\nwant %q", got, c.want)
			}
		})
	}
}

func TestBuildArgsMCPConfig(t *testing.T) {
	got, err := buildArgs(&Options{}, "/tmp/mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(got, "--mcp-config=/tmp/mcp.json") {
		t.Errorf("--mcp-config missing: %q", got)
	}
}

func contains(args []string, pair ...string) bool {
	for i := 0; i+len(pair) <= len(args); i++ {
		if reflect.DeepEqual(args[i:i+len(pair)], pair) {
			return true
		}
	}
	return false
}

func TestBuildArgsRejectsConflicts(t *testing.T) {
	bad := map[string]Options{
		"fork without resume":   {ForkSession: true},
		"resume and continue":   {Resume: "x", Continue: true},
		"negative max turns":    {MaxTurns: -1},
		"bad permission mode":   {PermissionMode: "yolo"},
		"session id and resume": {Resume: "x", SessionID: "y"},
		"comma in allowed tool": {AllowedTools: []string{"Bash(a,b)"}},
	}
	for name, o := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := buildArgs(&o, ""); err == nil || !strings.Contains(err.Error(), "claude:") {
				t.Errorf("want an options error, got %v", err)
			}
		})
	}
	// a value starting with '-' cannot be mistaken for a flag
	if got, _ := buildArgs(&Options{SystemPrompt: "--evil", Model: "-x"}, ""); !contains(got, "--system-prompt=--evil") || !contains(got, "--model=-x") {
		t.Errorf("values must use the = form: %q", got)
	}
	// fork may combine with session-id: the fork gets the given id
	if _, err := buildArgs(&Options{Resume: "x", ForkSession: true, SessionID: "y"}, ""); err != nil {
		t.Errorf("resume+fork+session-id is valid: %v", err)
	}
}
