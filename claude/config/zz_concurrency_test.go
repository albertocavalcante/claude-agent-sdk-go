package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The cleanup of one config must never remove the shared run dir another
// writer is about to use.
func TestConcurrentWriteAndCleanup(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	var wg sync.WaitGroup
	var mu sync.Mutex
	fails := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 300 {
				_, cl, err := WriteMCPConfig("", map[string]MCPServer{"a": {Command: "x"}})
				if err != nil {
					mu.Lock()
					fails++
					mu.Unlock()
					continue
				}
				cl()
			}
		}()
	}
	wg.Wait()
	if fails > 0 {
		t.Errorf("%d WriteMCPConfig failures under concurrent cleanup", fails)
	}
}

func TestCleanupNeverRemovesACallerDir(t *testing.T) {
	d := filepath.Join(t.TempDir(), "mine")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	_, cl, err := WriteMCPConfig(d, map[string]MCPServer{"a": {Command: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	cl()
	if _, err := os.Stat(d); err != nil {
		t.Errorf("cleanup removed the caller's directory: %v", err)
	}
}

func TestRunDirPathIsNotPredictable(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	// an attacker pre-creating the old predictable name achieves nothing
	_ = os.Symlink("/", filepath.Join(os.TempDir(), "claude-sdk-"+itoa(os.Getpid())))
	d, err := RunDir()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(d) == "claude-sdk-"+itoa(os.Getpid()) {
		t.Errorf("run dir %s has a predictable name", d)
	}
	d2, _ := RunDir()
	if d2 != d {
		t.Errorf("RunDir must be stable within a process: %s vs %s", d, d2)
	}
}

func TestSettingsFileRefusedWhenItIsASymlinkRacingIn(t *testing.T) {
	dir := writeRepo(t, `{}`, "")
	target := filepath.Join(t.TempDir(), "t.json")
	_ = os.WriteFile(target, []byte(`{}`), 0o644)
	_ = os.Remove(filepath.Join(dir, ".claude", "settings.json"))
	_ = os.Symlink(target, filepath.Join(dir, ".claude", "settings.json"))
	if _, err := readRegular(filepath.Join(dir, ".claude"), "settings.json"); err == nil {
		t.Error("a symlink must be refused by the open itself")
	}
}
