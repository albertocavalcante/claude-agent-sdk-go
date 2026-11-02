//go:build unix

package config

import (
	"errors"
	"io/fs"
	"os"
	"syscall"
)

func ownedByMe(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return !ok || int(st.Uid) == os.Getuid()
}

func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
