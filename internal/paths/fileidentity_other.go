//go:build !unix

package paths

import "os"

// Without a stat structure to read, the identity is the modification time
// alone: enough to notice a checkout replaced at the same path, which is the
// case the cache has to catch.
func identifyFile(path string) fileIdentity {
	info, err := os.Stat(path)
	if err != nil {
		return fileIdentity{}
	}
	return fileIdentity{mtime: info.ModTime().UnixNano()}
}
