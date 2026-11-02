package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func itoa(n int) string { return strconv.Itoa(n) }

var (
	runDirMu  sync.Mutex
	runDirVal string
)

// RunDir returns the private (0700) per-process directory that holds this
// SDK's temporary files (MCP configs, filtered settings). It is created with
// os.MkdirTemp under os.TempDir() on first use, so its name carries a random
// suffix and cannot be pre-created by someone else; it is left in place until
// the process exits (SweepStale removes what crashed processes leave).
func RunDir() (string, error) {
	runDirMu.Lock()
	defer runDirMu.Unlock()
	if runDirVal != "" && filepath.Dir(runDirVal) == filepath.Clean(os.TempDir()) {
		if fi, err := os.Lstat(runDirVal); err == nil && fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			return runDirVal, nil
		}
	}
	dir, err := os.MkdirTemp(os.TempDir(), "claude-sdk-"+itoa(os.Getpid())+"-")
	if err != nil {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil || !ownedByMe(fi) {
		return "", fmt.Errorf("config: %s is not owned by the current user", dir)
	}
	runDirVal = dir
	return dir, nil
}

// SweepStale removes per-process run directories left behind by SDK
// processes that died without cleaning up (crash, SIGKILL): directories named
// claude-sdk-<pid> under os.TempDir() whose process is gone and that have not
// been modified for maxAge. It returns how many it removed. Call it at
// daemon start.
func SweepStale(maxAge time.Duration) (int, error) {
	ents, err := os.ReadDir(os.TempDir())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range ents {
		rest, ok := strings.CutPrefix(e.Name(), "claude-sdk-")
		if !ok || !e.IsDir() {
			continue
		}
		pidStr, _, _ := strings.Cut(rest, "-")
		pid, err := strconv.Atoi(pidStr)
		if err != nil || processAlive(pid) {
			continue
		}
		fi, err := e.Info()
		if err != nil || time.Since(fi.ModTime()) < maxAge || !ownedByMe(fi) {
			continue
		}
		if os.RemoveAll(filepath.Join(os.TempDir(), e.Name())) == nil {
			n++
		}
	}
	return n, nil
}
