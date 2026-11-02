//go:build !unix

package config

import (
	"fmt"
	"io/fs"
	"os"
)

func openNoFollow(p string) (*os.File, error) {
	fi, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink", p)
	}
	return os.Open(p) //nolint:gosec // reading the project's own settings is the purpose
}
