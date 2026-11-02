package claude

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The recorded transcripts under testdata come from real CLI sessions. They
// were scrubbed before being committed: home and scratch paths became
// /home/user and /work, ids became placeholders, personal connector and
// skill catalogues became generic names. This gate keeps it that way. It is
// deliberately strict; fix the fixture, never the gate.

// Personal names are never written in this repository (it is public, and a
// hash of a single word is trivially reversible). Words that must not appear
// come from a file outside the tree named by $PRIVACY_WORDS, one per line;
// without it that part of the gate is skipped with a log line. The generic
// patterns below always run.

// privacyWords loads the out-of-tree word list.
func privacyWords(t *testing.T) []string {
	t.Helper()
	path := os.Getenv("PRIVACY_WORDS")
	if path == "" {
		t.Log("PRIVACY_WORDS is not set: skipping the personal-word check")
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("PRIVACY_WORDS: %v", err)
	}
	var words []string
	for _, l := range strings.Split(string(data), "\n") {
		if l = strings.ToLower(strings.TrimSpace(l)); l != "" && !strings.HasPrefix(l, "#") {
			words = append(words, l)
		}
	}
	return words
}

var (
	// generic leaks, spelled out because they name no person
	forbiddenPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)/users/[^/\s"]`),
		regexp.MustCompile(`(?i)/private/tmp`),
		regexp.MustCompile(`(?i)/private/var`),
		regexp.MustCompile(`(?i)/var/folders`),
		regexp.MustCompile(`(?i)/volumes/[^/\s"]`),
		regexp.MustCompile(`(?i)/home/(?:[^u/"\\]|u[^s])`), // /home/<anyone but "user">
		regexp.MustCompile(`sk-ant-`),
		regexp.MustCompile(`(?i)bearer\s`),
		regexp.MustCompile(`(?i)@gmail`),
		// a real account's connectors are a fingerprint of the account
		regexp.MustCompile(`mcp__claude_ai_`),
		regexp.MustCompile(`claude\.ai (?:[A-DF-Za-z]|E[^x])`),
	}
	emailRE     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)
	uuidRE      = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	identityKey = regexp.MustCompile(`(?i)^(account|organi[sz]ation|org|user)([_-]?(id|uuid))?$|(account|organi[sz]ation|user)[_-]?(id|uuid)`)
)

// leaks returns what is wrong with text, if anything. words are the
// out-of-tree personal words, matched case-insensitively anywhere in the text.
func leaks(text string, words []string) []string {
	var out []string
	lower := strings.ToLower(text)
	for _, w := range words {
		if strings.Contains(lower, w) {
			out = append(out, "a word from $PRIVACY_WORDS")
			break
		}
	}
	for _, re := range forbiddenPatterns {
		if m := re.FindStringIndex(text); m != nil {
			out = append(out, "pattern "+re.String()+" near "+snippet(text, m[0]))
		}
	}
	if m := emailRE.FindString(text); m != "" {
		out = append(out, "email-like string "+m)
	}
	return out
}

func TestTestdataHasNoPrivateData(t *testing.T) {
	root := ".."
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return fs.SkipDir
		}
		if !d.IsDir() && strings.Contains(filepath.ToSlash(path), "/testdata/") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no testdata files found; the gate would pass vacuously")
	}
	words := privacyWords(t)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range leaks(string(data), words) {
			t.Errorf("%s: %s", f, l)
		}
		if strings.HasSuffix(f, ".jsonl") {
			checkIdentityUUIDs(t, f, data)
		}
	}
}

func snippet(s string, i int) string {
	lo, hi := max(0, i-30), min(len(s), i+40)
	return s[lo:hi]
}

func checkIdentityUUIDs(t *testing.T, file string, data []byte) {
	t.Helper()
	for n, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Errorf("%s:%d: not JSON: %v", file, n+1, err)
			continue
		}
		walkIdentity(t, file, n+1, v, false)
	}
}

// walkIdentity fails on a UUID anywhere below a key that names an account,
// organization or user identity.
func walkIdentity(t *testing.T, file string, line int, v any, inIdentity bool) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			walkIdentity(t, file, line, child, inIdentity || identityKey.MatchString(k))
		}
	case []any:
		for _, child := range x {
			walkIdentity(t, file, line, child, inIdentity)
		}
	case string:
		if inIdentity && uuidRE.MatchString(x) {
			t.Errorf("%s:%d: UUID %q under an identity field", file, line, x)
		}
	}
}

// TestPrivacyGateCatchesLeaks proves the gate's matchers fire; without it a
// regex typo would make the real gate pass vacuously. Probes are generic.
func TestPrivacyGateCatchesLeaks(t *testing.T) {
	for _, s := range []string{"x /Users/bob y", "Bearer abc", "sk-ant-oat01", "a@b.com", "/Volumes/Disk", "/private/tmp/x",
		"/var/folders/ab/T/", "/home/bob/x", "mcp__claude_ai_Mail__x", "claude.ai Mail"} {
		if len(leaks(s, nil)) == 0 {
			t.Errorf("gate would miss %q", s)
		}
	}
	if len(leaks("owner zebracorn here", []string{"zebracorn"})) == 0 || len(leaks("x_ZEBRACORN-y", []string{"zebracorn"})) == 0 {
		t.Error("the word list is not applied")
	}
	for _, s := range []string{"/home/user/.claude", "claude.ai Example Docs", "mcp__example__echo", "x@y.z1", "/Users/", "/Volumes/"} {
		if l := leaks(s, nil); len(l) != 0 {
			t.Errorf("false positive on %q: %v", s, l)
		}
	}
	if !identityKey.MatchString("account") || !identityKey.MatchString("organizationUuid") || !identityKey.MatchString("user_id") {
		t.Error("identityKey regexp misses expected field names")
	}
}
