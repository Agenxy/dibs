//go:build !darwin && !linux

package main

import "os"

// Guest artifacts are published for Darwin and Linux only. Do not claim a
// credential-file permission check on an unsupported operating system.
func guestFileOwned(_ os.FileInfo) bool { return false }
