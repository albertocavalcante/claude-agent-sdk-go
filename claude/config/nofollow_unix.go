//go:build unix

package config

import (
	"os"
	"syscall"
)

func openNoFollow(p string) (*os.File, error) {
	return os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0) //nolint:gosec // reading the project's own settings is the purpose
}
