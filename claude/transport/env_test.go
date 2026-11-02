package transport

import (
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, e := range env {
		k, v, _ := strings.Cut(e, "=")
		m[k] = v
	}
	return m
}

var wantDropped = []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_BRIDGE_SESSION_ID",
	"CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_PID", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_EXECPATH"}

// A CLI started from inside Claude Code must not think it belongs to that
// session; user configuration variables stay.
func TestBuildEnvDropsParentSessionVars(t *testing.T) {
	base := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/cfg", "CLAUDE_CODE_OAUTH_TOKEN=tok", "CLAUDE_CODE_ENTRYPOINT=cli"}
	for _, k := range wantDropped {
		base = append(base, k+"=parent")
	}
	for name, in := range map[string][]string{"explicit": base, "inherited": nil} {
		t.Run(name, func(t *testing.T) {
			if in == nil {
				for _, e := range base {
					k, v, _ := strings.Cut(e, "=")
					t.Setenv(k, v)
				}
			}
			m := envMap(BuildEnv(in, nil, ""))
			for _, k := range wantDropped {
				if _, ok := m[k]; ok {
					t.Errorf("%s survived", k)
				}
			}
			if m["CLAUDE_CONFIG_DIR"] != "/cfg" || m["CLAUDE_CODE_OAUTH_TOKEN"] != "tok" {
				t.Errorf("user config vars must stay: %v", m)
			}
		})
	}
}

func TestBuildEnvEntrypointRule(t *testing.T) {
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli") // inherited from a parent Claude Code
	if got := envMap(BuildEnv(nil, nil, ""))["CLAUDE_CODE_ENTRYPOINT"]; got != DefaultEntrypoint {
		t.Errorf("an inherited entrypoint must be replaced, got %q", got)
	}
	if got := envMap(BuildEnv([]string{"CLAUDE_CODE_ENTRYPOINT=sdk-ts"}, nil, ""))["CLAUDE_CODE_ENTRYPOINT"]; got != "sdk-ts" {
		t.Errorf("an explicit entrypoint must win, got %q", got)
	}
	if got := envMap(BuildEnv([]string{"PATH=/bin"}, nil, ""))["CLAUDE_CODE_ENTRYPOINT"]; got != DefaultEntrypoint {
		t.Errorf("default entrypoint = %q", got)
	}
}
