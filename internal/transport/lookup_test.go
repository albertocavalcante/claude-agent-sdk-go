package transport

import (
	"testing"
)

func TestLookPath(t *testing.T) {
	// "echo" exists on all platforms.
	path, err := LookPath("echo")
	if err != nil {
		t.Fatalf("LookPath(echo) failed: %v", err)
	}
	if path == "" {
		t.Fatal("LookPath(echo) returned empty path")
	}

	// Second call should return cached result.
	path2, err := LookPath("echo")
	if err != nil {
		t.Fatalf("second LookPath(echo) failed: %v", err)
	}
	if path2 != path {
		t.Errorf("cache miss: got %q then %q", path, path2)
	}
}

func TestLookPathNotFound(t *testing.T) {
	_, err := LookPath("definitely-not-a-real-binary-xyz")
	if err == nil {
		t.Error("expected error for nonexistent binary")
	}
}
