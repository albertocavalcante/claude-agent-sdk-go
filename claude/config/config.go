// Package config is the safe launch policy for the Claude CLI.
//
// By default the CLI loads a repository's .claude/settings.json, its
// .mcp.json, hooks, env and apiKeyHelper even for an untrusted workspace; a
// repo can run commands at session start and redirect traffic (with the
// user's OAuth bearer token) to a host it picks via env.ANTHROPIC_BASE_URL.
// Workspace trust only gates permission allow rules. So this SDK defaults to
// the "user" setting source only, and offers FilteredProjectSettings to
// opt a project in without those vectors.
package config

import (
	"strings"
)

// SettingSource is a CLI settings source for --setting-sources.
type SettingSource string

// The CLI's setting sources.
const (
	SourceUser    SettingSource = "user"
	SourceProject SettingSource = "project"
	SourceLocal   SettingSource = "local"
)

// SettingSourcesArgs renders --setting-sources. nil (the default) means user
// only; a non-nil empty slice loads no sources at all.
func SettingSourcesArgs(sources []SettingSource) []string {
	if sources == nil {
		sources = []SettingSource{SourceUser}
	}
	parts := make([]string, len(sources))
	for i, s := range sources {
		parts[i] = string(s)
	}
	return []string{"--setting-sources=" + strings.Join(parts, ",")}
}
