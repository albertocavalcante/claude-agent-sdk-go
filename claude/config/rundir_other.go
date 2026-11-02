//go:build !unix

package config

import "io/fs"

func ownedByMe(fs.FileInfo) bool { return true }

// Without a portable liveness probe nothing is swept.
func processAlive(int) bool { return true }
