//go:build darwin || linux

package main

import (
	"os"
	"syscall"
)

func guestFileOwned(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && int64(s.Uid) == int64(os.Getuid())
}
