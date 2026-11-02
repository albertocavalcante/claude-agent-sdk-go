package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTempFilesLiveInAPrivatePerProcessDir(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	p, cleanup, err := WriteMCPConfig("", map[string]MCPServer{"a": {Command: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	dir := filepath.Dir(p)
	if !strings.HasPrefix(filepath.Base(dir), "claude-sdk-") {
		t.Errorf("dir = %s", dir)
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("run dir mode %v err %v", fi.Mode().Perm(), err)
	}
	sp, c2, err := FilteredProjectSettings(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c2()
	if filepath.Dir(sp) != dir {
		t.Errorf("settings file in %s, want %s", filepath.Dir(sp), dir)
	}
}

func TestSweepStaleRemovesDeadProcessDirs(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	dead := filepath.Join(tmp, "claude-sdk-2147483646") // no such pid
	live := filepath.Join(tmp, "claude-sdk-"+itoa(os.Getpid()))
	unrelated := filepath.Join(tmp, "other-dir")
	for _, d := range []string{dead, live, unrelated} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-48 * time.Hour)
		_ = os.Chtimes(d, old, old)
	}
	n, err := SweepStale(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("swept %d", n)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Error("dead process dir survived")
	}
	for _, d := range []string{live, unrelated} {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("%s must be kept: %v", d, err)
		}
	}
}
