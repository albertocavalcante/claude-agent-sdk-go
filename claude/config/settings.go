package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// maxSettingsBytes caps one settings file.
const maxSettingsBytes = 1 << 20

// allowedKeys is the ALLOWLIST of top-level settings keys that reach the
// filtered file. Everything else is dropped and reported, so a key added to
// the CLI later is dropped until someone reviews it (fail closed).
//
//   - includeCoAuthoredBy: a boolean for commit trailers, inert.
//   - permissions: only deny and ask rules, which can only make the session
//     stricter (see allowedPermissionKeys). allow, defaultMode and
//     additionalDirectories would let a repo approve its own tool calls or
//     widen file access.
//   - hooks: only groups whose sha256 is allowlisted by the caller.
//
// Deliberately absent: cleanupPeriodDays (it deletes transcripts globally and
// --settings outranks the user's own settings), env, model (the caller chooses), apiKeyHelper and the
// other *Helper keys, awsAuthRefresh, awsCredentialExport, statusLine,
// subagentStatusLine, fileSuggestion (all run commands), enabledPlugins,
// extraKnownMarketplaces (install code), sandbox (can switch the sandbox
// off), mcpServers and the *McpjsonServers keys, forceLoginOrgUUID.
var allowedKeys = map[string]bool{
	"includeCoAuthoredBy": true,
	"permissions":         true,
	"hooks":               true,
}

var allowedPermissionKeys = map[string]bool{"deny": true, "ask": true}

// HookInfo identifies one hook group of a project's settings for allowlisting.
type HookInfo struct {
	// Event is the hook event (SessionStart, PreToolUse, ...).
	Event string
	// Hash is the lowercase hex sha256 of the group's canonical JSON (compact,
	// object keys sorted), the value to put in the allowlist.
	Hash string
	// Group is the hook group as written (matcher + hooks), for display.
	Group json.RawMessage
}

// FilterResult is the outcome of FilterProjectSettings.
type FilterResult struct {
	// JSON is the filtered settings document.
	JSON []byte
	// Dropped lists what was removed, as dotted paths, so a UI can say so.
	Dropped []string
}

func readSettings(dir string) (map[string]any, error) {
	merged := map[string]any{}
	for _, name := range []string{"settings.json", "settings.local.json"} {
		b, err := readRegular(filepath.Join(dir, ".claude"), name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(b))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return nil, fmt.Errorf("config: %s: %w", name, err)
		}
		mergeSettings(merged, m)
	}
	return merged, nil
}

// readRegular reads dir/name, refusing symlinks (the directory included) and
// files over maxSettingsBytes: a hostile repo must not point the reader at
// another file or at something huge.
func readRegular(dir, name string) ([]byte, error) {
	di, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if di.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("config: %s is a symlink", dir)
	}
	p := filepath.Join(dir, name)
	f, err := openNoFollow(p) // the open itself refuses a symlink: no Lstat/Open race
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("config: %s: %w", p, err)
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("config: %s is not a regular file", p)
	}
	if fi.Size() > maxSettingsBytes {
		return nil, fmt.Errorf("config: %s is larger than %d bytes", p, maxSettingsBytes)
	}
	return io.ReadAll(io.LimitReader(f, maxSettingsBytes+1))
}

// mergeSettings overlays src on dst: hooks and permission rule lists are
// concatenated, everything else is replaced.
func mergeSettings(dst, src map[string]any) {
	for k, v := range src {
		switch k {
		case "hooks":
			sh, ok1 := v.(map[string]any)
			dh, ok2 := dst["hooks"].(map[string]any)
			if !ok1 || (dst["hooks"] != nil && !ok2) { // malformed: keep it so the filter reports and drops it
				dst["hooks"] = v
				continue
			}
			if dh == nil {
				dh = map[string]any{}
			}
			for ev, groups := range sh {
				a, _ := dh[ev].([]any)
				b, _ := groups.([]any)
				dh[ev] = append(a, b...)
			}
			dst["hooks"] = dh
		case "permissions":
			sp, ok1 := v.(map[string]any)
			dp, ok2 := dst["permissions"].(map[string]any)
			if !ok1 || (dst["permissions"] != nil && !ok2) {
				dst["permissions"] = v
				continue
			}
			if dp == nil {
				dp = map[string]any{}
			}
			for pk, pv := range sp {
				if arr, ok := pv.([]any); ok {
					old, _ := dp[pk].([]any)
					dp[pk] = append(old, arr...)
				} else {
					dp[pk] = pv
				}
			}
			dst["permissions"] = dp
		default:
			dst[k] = v
		}
	}
}

// canonical returns compact JSON with object keys sorted.
func canonical(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil { // map keys marshal sorted
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func hashOf(v any) (string, error) {
	c, err := canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:]), nil
}

// ListHooks returns the hook groups declared by the project's
// .claude/settings.json and settings.local.json with their allowlist hashes.
func ListHooks(projectDir string) ([]HookInfo, error) {
	m, err := readSettings(projectDir)
	if err != nil {
		return nil, err
	}
	var out []HookInfo
	hooks, _ := m["hooks"].(map[string]any)
	events := make([]string, 0, len(hooks))
	for ev := range hooks {
		events = append(events, ev)
	}
	sort.Strings(events)
	for _, ev := range events {
		groups, _ := hooks[ev].([]any)
		for _, g := range groups {
			h, err := hashOf(g)
			if err != nil {
				return nil, err
			}
			c, _ := canonical(g)
			out = append(out, HookInfo{Event: ev, Hash: h, Group: c})
		}
	}
	return out, nil
}

// FilterProjectSettings reads <projectDir>/.claude/settings.json and
// settings.local.json and returns a version that is safe to pass to
// --settings:
//
//   - env is dropped (at any depth), and so is any key ending in _BASE_URL;
//   - apiKeyHelper, awsAuthRefresh, awsCredentialExport, otelHeadersHelper,
//     statusLine and repo-chosen MCP server keys are dropped;
//   - permissions.allow and permissions.defaultMode are dropped: a repo never
//     grants itself approvals (deny and ask rules are kept);
//   - hook groups are dropped unless the sha256 of their canonical JSON is in
//     allowHookHashes (an optional "sha256:" prefix is accepted).
//
// Opting a project in through allowlisted hooks still grants it code
// execution; show ListHooks output to whoever decides.
func FilterProjectSettings(projectDir string, allowHookHashes []string) (*FilterResult, error) {
	m, err := readSettings(projectDir)
	if err != nil {
		return nil, err
	}
	allow := map[string]bool{}
	for _, h := range allowHookHashes {
		allow[strings.ToLower(strings.TrimPrefix(h, "sha256:"))] = true
	}
	var dropped []string
	for k := range m {
		if !allowedKeys[k] {
			delete(m, k)
			dropped = append(dropped, k)
		}
	}
	if raw, ok := m["permissions"]; ok {
		perms, isMap := raw.(map[string]any)
		if !isMap {
			delete(m, "permissions")
			dropped = append(dropped, "permissions")
		} else {
			clean := map[string]any{}
			for k, v := range perms {
				arr, isArr := v.([]any)
				if !allowedPermissionKeys[k] || !isArr {
					dropped = append(dropped, "permissions."+k)
					continue
				}
				rules := []any{} // an empty list stays [], never null
				for _, r := range arr {
					if _, isStr := r.(string); isStr {
						rules = append(rules, r)
					} else {
						dropped = append(dropped, "permissions."+k)
					}
				}
				clean[k] = rules
			}
			m["permissions"] = clean
		}
	}
	if raw, ok := m["hooks"]; ok {
		hooks, isMap := raw.(map[string]any)
		if !isMap {
			delete(m, "hooks")
			dropped = append(dropped, "hooks")
		} else {
			for ev, v := range hooks {
				groups, isArr := v.([]any)
				var keep []any
				if !isArr {
					dropped = append(dropped, "hooks."+ev)
				}
				for _, g := range groups {
					h, err := hashOf(g)
					if err != nil {
						return nil, err
					}
					if allow[h] {
						keep = append(keep, g)
					} else {
						dropped = append(dropped, "hooks."+ev)
					}
				}
				if len(keep) == 0 {
					delete(hooks, ev)
				} else {
					hooks[ev] = keep
				}
			}
			if len(hooks) == 0 {
				delete(m, "hooks")
			}
		}
	}
	scrub(m, "", &dropped)
	sort.Strings(dropped)
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	return &FilterResult{JSON: out, Dropped: dropped}, nil
}

// scrub removes env-like maps and *_BASE_URL keys at every depth.
func scrub(v any, path string, dropped *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			p := k
			if path != "" {
				p = path + "." + k
			}
			if strings.EqualFold(k, "env") || strings.HasSuffix(strings.ToUpper(k), "_BASE_URL") {
				delete(x, k)
				*dropped = append(*dropped, p)
				continue
			}
			scrub(child, p, dropped)
		}
	case []any:
		for _, child := range x {
			scrub(child, path, dropped)
		}
	}
}

// FilteredProjectSettings is FilterProjectSettings written to a 0600 temp
// file ready for --settings (claude.Options.Settings). cleanup removes it and
// is idempotent.
func FilteredProjectSettings(projectDir string, allowHookHashes []string) (path string, cleanup func(), err error) {
	res, err := FilterProjectSettings(projectDir, allowHookHashes)
	if err != nil {
		return "", nil, err
	}
	return writeTemp("", "claude-settings-*.json", res.JSON)
}
