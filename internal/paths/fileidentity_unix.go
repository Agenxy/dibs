//go:build unix

package paths

import (
	"os"
	"syscall"
)

func identifyFile(path string) fileIdentity {
	info, err := os.Stat(path)
	if err != nil {
		return fileIdentity{}
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}
	}
	return fileIdentity{
		device: int64(st.Dev), inode: st.Ino,
		ctime: ctimeOf(st), mtime: info.ModTime().UnixNano(),
	}
}
