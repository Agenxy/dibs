// Package sibling finds the other Agenxy programs Dibs depends on.
//
// Dibs asks Supgang who a computer is and Remap what a board is called, by
// running their binaries. A person's shell finds those on PATH; the daemon
// does not have that shell. launchd starts dibd with a PATH of a few system
// directories, so a `supgang` installed in ~/.local/bin was invisible to the
// daemon and visible to `dibs doctor` run from a terminal: the daemon fell
// back to its own identity, doctor reported the drift, and both were right.
// The fix is to look where the siblings are installed, not only where the
// shell says.
package sibling

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Find returns the path of the named sibling: PATH first, then the places
// the Agenxy installers and the common package managers put binaries, which
// a service's environment does not list. Empty when it is nowhere.
func Find(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, dir := range installDirs() {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// installDirs is where a sibling is put on this machine when it is not on
// the caller's PATH: the per-user install prefix, then the system ones.
func installDirs() []string {
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"), filepath.Join(home, ".cargo", "bin"))
	}
	dirs = append(dirs, "/usr/local/bin")
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, "/opt/homebrew/bin")
	}
	return dirs
}
