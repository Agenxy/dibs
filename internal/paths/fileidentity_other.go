//go:build windows

package paths

import (
	"os"

	"golang.org/x/sys/windows"
)

// identifyFile on Windows: the volume and file index, which is what the
// filesystem calls this file as opposed to what it is named, plus the
// creation time, so a checkout deleted and recreated at the same path (a
// new file index, a new creation time) is not mistaken for the old one.
func identifyFile(path string) fileIdentity {
	info, err := os.Stat(path)
	if err != nil {
		return fileIdentity{}
	}
	id := fileIdentity{mtime: info.ModTime().UnixNano()}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return id
	}
	h, err := windows.CreateFile(p, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return id
	}
	defer func() { _ = windows.CloseHandle(h) }()
	var fi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &fi); err != nil {
		return id
	}
	id.device = int64(fi.VolumeSerialNumber)
	id.inode = uint64(fi.FileIndexHigh)<<32 | uint64(fi.FileIndexLow)
	id.ctime = fi.CreationTime.Nanoseconds()
	return id
}
