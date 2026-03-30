package transport

import (
	"os/exec"
	"sync"
)

// binaryCache caches exec.LookPath results to avoid repeated PATH lookups.
// The Claude CLI binary location doesn't change during a process lifetime.
var binaryCache sync.Map // map[string]string

// LookPath resolves a binary name to its absolute path, caching the result.
// If the name is already an absolute path, it is returned as-is.
func LookPath(name string) (string, error) {
	if v, ok := binaryCache.Load(name); ok {
		if s, isStr := v.(string); isStr {
			return s, nil
		}
	}

	path, err := exec.LookPath(name)
	if err != nil {
		return "", err
	}

	binaryCache.Store(name, path)
	return path, nil
}
